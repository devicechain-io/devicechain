// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// deleteFlakyJS fails DeleteConsumer with a broker timeout while failDeletes is positive
// (counting down) or negative (always), as the broker does across a roll.
type deleteFlakyJS struct {
	nats.JetStreamContext
	failDeletes atomic.Int32
	deleteCalls atomic.Int32
}

func (f *deleteFlakyJS) DeleteConsumer(stream, consumer string, opts ...nats.JSOpt) error {
	f.deleteCalls.Add(1)
	if n := f.failDeletes.Load(); n < 0 || (n > 0 && f.failDeletes.Add(-1) >= 0) {
		return nats.ErrTimeout
	}
	return f.JetStreamContext.DeleteConsumer(stream, consumer, opts...)
}

// stageOldBuildCapture creates the capture stream and the durable the previous build left on it.
func stageOldBuildCapture(t *testing.T, srvURL, area string, staged bool) nats.JetStreamContext {
	t.Helper()
	nc, err := nats.Connect(srvURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name: captureStreamOn("test"), Subjects: DeviceEventsSubjects("test"), Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	if staged {
		require.NoError(t, oldBuildBind(js, DurableName("test", area, captureSuffix)))
	}
	return js
}

// GUARD: a delete of the earlier build's durable that fails transiently is retried and succeeds;
// the reader starts, the old name is gone and the new durable is unfiltered.
func TestATransientFailureDeletingTheOldCaptureDurableIsRetried(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("legacy-transient")
	js := stageOldBuildCapture(t, srv.ClientURL(), area, true)

	var flaky *deleteFlakyJS
	nmgr, _ := recorderRigWith(t, srv, area, recordNothing, func(n *NatsManager) {
		flaky = &deleteFlakyJS{JetStreamContext: n.js}
		flaky.failDeletes.Store(2)
		n.js = flaky
	}, captureSuffix)

	require.EqualValues(t, 3, flaky.deleteCalls.Load(), "two timeouts then the delete that lands")
	_, err := js.ConsumerInfo(captureStreamOn("test"), DurableName("test", area, captureSuffix))
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old-named durable was left behind")
	info := captureDurable(t, nmgr.js, area)
	require.Empty(t, info.Config.FilterSubjects)
}

// GUARD: not-found means done. With no earlier-build durable the delete is asked once, the
// start is clean, and nothing is retried.
func TestAnAbsentOldCaptureDurableIsANoOp(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("legacy-absent")
	stageOldBuildCapture(t, srv.ClientURL(), area, false)

	var flaky *deleteFlakyJS
	nmgr, _ := recorderRigWith(t, srv, area, recordNothing, func(n *NatsManager) {
		flaky = &deleteFlakyJS{JetStreamContext: n.js}
		n.js = flaky
	}, captureSuffix)

	require.EqualValues(t, 1, flaky.deleteCalls.Load())
	captureDurable(t, nmgr.js, area)
}

// GUARD: a delete that cannot be confirmed does NOT stop the service starting (on main it was
// fatal: NewReader returned the error and the pod crashed). Beside the old durable the work queue
// refuses the new durable's create, so the reader starts unbound and its read loop re-binds with
// backoff; once the broker answers the delete, the old name is gone, the new durable binds and the
// message stored all along is delivered exactly once.
func TestAnUnconfirmedDeleteOfTheOldCaptureDurableDoesNotFailTheStart(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("legacy-permanent")
	js := stageOldBuildCapture(t, srv.ClientURL(), area, true)
	legacy := DurableName("test", area, captureSuffix)
	_, err := js.Publish(DeviceEventsSubject("test", "acme", "before"), []byte("before"))
	require.NoError(t, err)

	var flaky *deleteFlakyJS
	nmgr, readers := recorderRigWith(t, srv, area, recordNothing, func(n *NatsManager) {
		flaky = &deleteFlakyJS{JetStreamContext: n.js}
		flaky.failDeletes.Store(-1)
		n.js = flaky
	}, captureSuffix)
	require.GreaterOrEqual(t, int(flaky.deleteCalls.Load()), legacyDeleteAttempts, "the delete was not retried")

	// The old durable is still there and so is the work queue's refusal of a second consumer
	// beside it: the new durable does not exist, the old one holds the message.
	_, err = js.ConsumerInfo(captureStreamOn("test"), legacy)
	require.NoError(t, err)
	_, err = js.ConsumerInfo(captureStreamOn("test"), CaptureDurableName("test", area, captureSuffix))
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the work queue admitted a second overlapping consumer")

	// The broker answers again: the reader heals on its own.
	flaky.failDeletes.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	msg, err := readers[0].ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "before", string(msg.Value))
	require.NoError(t, msg.Ack())
	_, err = js.ConsumerInfo(captureStreamOn("test"), legacy)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old-named durable is still there after the heal")
	require.Empty(t, captureDurable(t, nmgr.js, area).Config.FilterSubjects)
}

// GUARD: the rolling-upgrade hazard stays closed with the old durable unconfirmed. An old pod
// re-filtering in place cannot split messages: while the old durable stands, the new one cannot be
// created (above); once the new one stands, the old build's create is refused (the existing
// TestARollingUpgradeBesideAnOldPodLeavesTheCaptureUnfiltered). This pins the first half
// directly, on the server: a second consumer on the work queue is refused.
func TestTheWorkQueueRefusesAnUnfilteredConsumerBesideTheOldDurable(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("legacy-refused")
	js := stageOldBuildCapture(t, srv.ClientURL(), area, true)
	_, err := js.AddConsumer(captureStreamOn("test"), &nats.ConsumerConfig{
		Durable: CaptureDurableName("test", area, captureSuffix), AckPolicy: nats.AckExplicitPolicy,
	})
	require.Error(t, err)
}
