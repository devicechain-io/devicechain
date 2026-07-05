// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isConsumerGone must fire for every "the durable is no longer usable" error so the
// reader self-heals (re-binds) rather than surfacing the error and hot-spinning, and
// must NOT fire for a transient timeout or a shutdown-time close (those have their
// own handling).
func TestIsConsumerGone(t *testing.T) {
	gone := []error{
		nats.ErrConsumerDeleted,
		nats.ErrConsumerNotFound,
		nats.ErrConsumerNotActive,
		nats.ErrNoResponders,
		// Still detected when wrapped, since the reader wraps/propagates Fetch errors.
		fmt.Errorf("fetch failed: %w", nats.ErrConsumerDeleted),
	}
	for _, err := range gone {
		assert.True(t, isConsumerGone(err), "expected consumer-gone for %v", err)
	}

	notGone := []error{
		nats.ErrTimeout,
		nats.ErrConnectionClosed,
		nats.ErrSubscriptionClosed,
		nats.ErrConnectionDraining,
		io.EOF,
		errors.New("some other error"),
		nil,
	}
	for _, err := range notGone {
		assert.False(t, isConsumerGone(err), "expected NOT consumer-gone for %v", err)
	}
}

// readerFor is a helper: create a durable reader over a suffix and return it plus
// the stream/durable names the fix manages out-of-band.
func readerFor(t *testing.T, nmgr *NatsManager, suffix string) (*natsReader, string, string) {
	t.Helper()
	_, err := nmgr.NewReader(suffix)
	require.NoError(t, err)
	r := nmgr.readers[len(nmgr.readers)-1]
	return r, StreamName(nmgr.Microservice.InstanceId, suffix), DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, suffix)
}

// The core guarantee of the fix: a bound reader's Unsubscribe (what every pod does on
// shutdown, and what a rolling update's terminating pod does) must NOT delete the
// shared durable consumer — otherwise the surviving/new pod is left bound to a
// deleted consumer. Before the fix (PullSubscribe-owned consumer) this Unsubscribe
// deleted it.
func TestBoundReaderUnsubscribeDoesNotDeleteDurable(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	r, stream, durable := readerFor(t, nmgr, "alarm-events")

	// Present before Unsubscribe.
	_, err := nmgr.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)

	// Unsubscribe the bound subscription — the shutdown path.
	require.NoError(t, r.sub.Load().Unsubscribe())

	// Still present: the durable survived, so another replica / the next pod keeps
	// consuming from where this one left off.
	_, err = nmgr.js.ConsumerInfo(stream, durable)
	assert.NoError(t, err, "durable consumer must survive a bound reader's Unsubscribe")
}

// The self-heal path: if the durable consumer does go away (an older pre-fix pod's
// Unsubscribe during the transition rollout, or a broker restart), ReadMessage
// re-binds and keeps delivering rather than hot-spinning on a dead subscription.
func TestReaderSelfHealsAfterConsumerDeleted(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	r, stream, durable := readerFor(t, nmgr, "alarm-events")
	writer, err := nmgr.NewWriter("alarm-events")
	require.NoError(t, err)

	ctx := core.WithTenant(context.Background(), "tenant1")
	require.NoError(t, writer.WriteMessages(ctx, Message{Value: []byte("first")}))

	// Read + ack the first message normally.
	readCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, err := r.ReadMessage(readCtx)
	require.NoError(t, err)
	assert.Equal(t, "first", string(msg.Value))
	require.NoError(t, msg.Ack())

	// Simulate the rollout hazard: something deletes the durable out-of-band.
	require.NoError(t, nmgr.js.DeleteConsumer(stream, durable))

	// Publish more traffic and read again: ReadMessage must detect the gone consumer,
	// re-bind (recreating it), and deliver — not error or hang. (The recreated
	// consumer is DeliverAll, so it replays the retained stream; we only assert that
	// delivery recovers.)
	require.NoError(t, writer.WriteMessages(ctx, Message{Value: []byte("second")}))
	msg, err = r.ReadMessage(readCtx)
	require.NoError(t, err, "reader must self-heal after the consumer was deleted")
	assert.Contains(t, []string{"first", "second"}, string(msg.Value))
	require.NoError(t, msg.Ack())

	// And the consumer exists again (was recreated by the self-heal).
	_, err = nmgr.js.ConsumerInfo(stream, durable)
	assert.NoError(t, err)
}
