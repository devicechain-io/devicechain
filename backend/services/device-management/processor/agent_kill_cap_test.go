// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Verify the broker premise: an unacked final delivery ceases to be backlog.
// A caught-up poll alone cannot prove that every kill was applied.
func TestKillDurableDropsUnackedAtDeliveryCap(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true,
		JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	subject := messaging.ScopedSubject("test", "acme", streams.AgentActuationKills)
	_, err = js.AddStream(&nats.StreamConfig{Name: "kills", Subjects: []string{subject}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	_, err = js.AddConsumer("kills", &nats.ConsumerConfig{
		Durable: "kill-consumer", AckPolicy: nats.AckExplicitPolicy,
		AckWait: 25 * time.Millisecond, MaxDeliver: streams.ConsumerMaxDeliver,
	})
	require.NoError(t, err)
	sub, err := js.PullSubscribe(subject, "kill-consumer", nats.Bind("kills", "kill-consumer"))
	require.NoError(t, err)
	_, err = js.Publish(subject, []byte(`{"kill":"unacked"}`))
	require.NoError(t, err)
	for delivery := 1; delivery <= streams.ConsumerMaxDeliver; delivery++ {
		msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
		require.NoError(t, err)
		meta, err := msgs[0].Metadata()
		require.NoError(t, err)
		require.EqualValues(t, delivery, meta.NumDelivered)
	}
	// The next pull lets the broker retire the exhausted pending entry.
	_, err = sub.Fetch(1, nats.MaxWait(200*time.Millisecond))
	require.ErrorIs(t, err, nats.ErrTimeout)
	info, err := js.ConsumerInfo("kills", "kill-consumer")
	require.NoError(t, err)
	require.Zero(t, info.NumPending)
	require.Zero(t, info.NumAckPending)
}

func killCapMessage(t *testing.T, now time.Time, delivery int, ack *killAck) messaging.Message {
	t.Helper()
	body, err := actiongate.EncodeKillEvent(actiongate.KillEvent{
		EventID: "failed-kill", Tenant: "acme", ActuationGeneration: 4,
		Cause: actiongate.KillCauseTenantDisabled, At: now,
	})
	require.NoError(t, err)
	return messaging.NewConsumedMessage(messaging.ScopedSubject("test", "acme", streams.AgentActuationKills), body, delivery, nil, ack).
		WithOrigin(messaging.Origin{Suffix: streams.AgentActuationKills, Stream: "test_agent-actuation-kills",
			Consumer: "test_device-management_agent-actuation-kills", Seq: 7})
}

type killCapDeadWriter struct {
	deadRecorder
	feed     *model.KillFeed
	err      error
	sawFresh bool
}

func (w *killCapDeadWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.sawFresh = w.sawFresh || w.feed.Fresh(w.feed.Now())
	_ = w.deadRecorder.WriteMessages(ctx, msgs...)
	return w.err
}

func TestAgentKillFailureAtDeliveryCapLatchesFeedAndDeadLetters(t *testing.T) {
	for _, sinkMode := range []string{"enabled", "disabled", "failed"} {
		t.Run(sinkMode, func(t *testing.T) {
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			feed := model.NewKillFeed(time.Second, func() time.Time { return now })
			r := &killPollReader{}
			api := &killAPI{err: errors.New("database unavailable")}
			writer := &killCapDeadWriter{feed: feed}
			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			var sink *deadletter.Sink
			if sinkMode != "disabled" {
				sink = deadletter.NewProducer(ms).NewSink(writer)
			}
			if sinkMode == "failed" {
				writer.err = errors.New("dead-letter broker unavailable")
			}
			c := NewAgentKillConsumer(ms, r, api, feed, sink)
			c.poll(context.Background())
			require.True(t, feed.Fresh(now))
			for delivery := 1; delivery <= messaging.MaxDeliver; delivery++ {
				ack := &killAck{}
				c.handle(context.Background(), killCapMessage(t, now, delivery, ack))
				require.Equal(t, delivery, api.calls)
				if delivery < messaging.MaxDeliver {
					require.Zero(t, ack.calls, "non-final failure must remain unacked")
					require.Empty(t, writer.msgs, "non-final failure must not be dead-lettered")
				}
			}
			// Final failure must invalidate even a recent heartbeat, before writing the letter.
			require.False(t, feed.Fresh(now), "an unapplied final kill must immediately fail closed")
			require.False(t, writer.sawFresh, "latch before the dead-letter write can block or fail")
			if sinkMode == "enabled" {
				require.Len(t, writer.msgs, 1)
				e, err := deadletter.Unmarshal(writer.msgs[0].Value)
				require.NoError(t, err)
				require.Equal(t, deadletter.ReasonUnprocessable, e.Reason)
				require.Equal(t, deadletter.KindControlFact, e.Kind)
				require.Equal(t, "device-management", e.Source)
				require.Equal(t, messaging.MaxDeliver, e.Attempts)
				require.EqualValues(t, 7, e.Sequence)
				require.Equal(t, "failed-kill", e.Reference)
				require.Contains(t, e.Summary, "repeat the kill")
				require.Contains(t, e.Detail, api.err.Error())
				require.Equal(t, []string{"acme"}, writer.tenants)
			}
			// Model the broker after the cap: nothing pending or unacked, forever.
			// Recovery and a second consumer sharing the process feed cannot clear the latch.
			api.err = nil
			other := NewAgentKillConsumer(ms, r, api, feed, sink)
			other.handle(context.Background(), killCapMessage(t, now, 1, &killAck{}))
			for range 3 {
				now = now.Add(time.Hour)
				other.poll(context.Background())
				feed.Beat(now)
				require.False(t, feed.Fresh(now), "caught-up polls and successful kills cannot clear the process latch")
			}
		})
	}
}

func TestAgentKillNonFinalFailureCanRecover(t *testing.T) {
	now := time.Now()
	feed := model.NewKillFeed(time.Second, func() time.Time { return now })
	api := &killAPI{err: errors.New("database unavailable")}
	r := &killPollReader{unacked: 1}
	c := &AgentKillConsumer{Reader: r, Api: api, Feed: feed}
	ack := &killAck{}
	c.handle(context.Background(), killCapMessage(t, now, messaging.MaxDeliver-1, ack))
	require.Zero(t, ack.calls)
	c.poll(context.Background())
	require.False(t, feed.Fresh(now))
	api.err = nil
	c.handle(context.Background(), killCapMessage(t, now, messaging.MaxDeliver, ack))
	require.Equal(t, 1, ack.calls)
	r.unacked = 0
	c.poll(context.Background())
	require.True(t, feed.Fresh(now), "a recovered non-final failure must not latch the feed")
}
