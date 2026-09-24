// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-notification-management/model"
)

// capacityMessage reads one message through a REAL capacity reader over an embedded JetStream
// whose durables use the given AckWait, and returns it. Its AckDeadline is the broker's: the
// fetch time plus that AckWait. There is no other way to get one — the deadline is carried only
// by a message a capacity reader produced, which is the property being relied on.
func capacityMessage(t *testing.T, ackWait time.Duration) messaging.Message {
	t.Helper()
	return capacityMessageWith(t, ackWait, []byte("x"))
}

// capacityMessageWith is capacityMessage carrying the given body.
func capacityMessageWith(t *testing.T, ackWait time.Duration, body []byte) messaging.Message {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	area := fmt.Sprintf("ackdeadline-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: area, FunctionalArea: "notification-management"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}

	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := m.NewReader(streams.AlarmEvents, messaging.ReaderWithCapacity(1))
		reader = r
		return err
	})
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	nmgr.SetAckWaitForTesting(t, ackWait)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	writer, err := nmgr.NewWriter(streams.AlarmEvents)
	require.NoError(t, err)
	require.NoError(t, writer.WriteMessages(tenantScoped(), messaging.Message{Value: body}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	t.Cleanup(msg.Release)
	require.False(t, msg.AckDeadline().IsZero(), "a capacity reader's message must carry an AckDeadline")
	return msg
}

// A dispatch whose message's broker clock has already partly run is bounded by what is LEFT of
// it, not by a fresh budget counted from now.
//
// The message carries an AckDeadline 30s out, well inside the 40s dispatchBudget, so the budget
// alone would give the delivery ~40s — ten seconds past the moment the broker hands the alarm to a
// second worker. Capped, the delivery must end dispatchMargin (20s) before that deadline: ~10s
// away. The instrument is the deadline the adapter is handed, as in
// TestDispatchSpendsTheWholeDispatchBudget, which is the counterweight for a message with none.
func TestDispatchDeadlineIsCappedByAckDeadline(t *testing.T) {
	msg := capacityMessage(t, 30*time.Second)
	want := time.Until(msg.AckDeadline().Add(-dispatchMargin))
	require.Less(t, want, dispatchBudget-5*time.Second,
		"the fixture's AckDeadline must be tighter than dispatchBudget, or this test measures nothing")

	api := fencedApi(t)
	ctx := messaging.WithAckDeadline(tenantScoped(), msg)
	seedRoutedAlarm(t, api, ctx)

	da := &deadlineRecordingAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: da})
	n.api = api
	n.store = &fakeSecretStore{}
	n.timeout = 2 * time.Minute // far larger than either bound

	require.NoError(t, n.dispatch(ctx, raisedEvent("CRITICAL")))
	require.Equal(t, 1, da.calls, "the fixture must reach a delivery or this test measures nothing")
	require.True(t, da.hadDeadline, "the delivery ran with no deadline at all")
	require.LessOrEqual(t, da.remaining, want,
		"the delivery's deadline was %v away; it must end dispatchMargin before the message's "+
			"AckDeadline (%v away), not a full dispatchBudget from now", da.remaining, want)
	require.Greater(t, da.remaining, want-2*time.Second,
		"the delivery's deadline was %v away, far tighter than the %v the AckDeadline allows", da.remaining, want)
}

// ackDeadlineRecordingNotifier records the AckDeadline on the context each Notify is handed.
type ackDeadlineRecordingNotifier struct {
	calls    int
	deadline time.Time
	had      bool
}

func (r *ackDeadlineRecordingNotifier) Notify(ctx context.Context, _ *dmmodel.AlarmStateChangeEvent) error {
	r.calls++
	r.deadline, r.had = messaging.AckDeadlineFrom(ctx)
	return nil
}

// The worker hands the Notifier the message's AckDeadline. TestDispatchDeadlineIsCappedByAckDeadline
// shows the Notifier bounds a dispatch by it when it is there; this is the caller's half — that it
// IS there for a message from a capacity reader. Without it the Notifier falls back to a budget
// counted from dequeue, which is the one a message that reached its worker late can overrun, paging
// past its redelivery and sending twice. The instrument is the exact value, not "some deadline".
func TestDispatchOneHandsTheNotifierTheAckDeadline(t *testing.T) {
	msg := capacityMessageWith(t, 30*time.Second, validEventBytes(t))
	rec := &ackDeadlineRecordingNotifier{}
	newTestProcessor(rec).dispatchOne(context.Background(), msg)

	require.Equal(t, 1, rec.calls, "the fixture must reach the Notifier or this test measures nothing")
	require.True(t, rec.had, "the Notifier was handed no AckDeadline for a message from a capacity reader")
	require.Equal(t, msg.AckDeadline(), rec.deadline,
		"the Notifier must be handed the message's own AckDeadline")
}
