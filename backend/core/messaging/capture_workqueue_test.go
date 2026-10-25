// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The device intake capture stream is a work queue: an ack removes the message, a give-up is
// lettered and then removed, and a stream created under the earlier retention is drained and
// recreated. Each claim is about the broker, so each runs against the embedded server.

const captureSuffix = streams.DeviceEventsCapture

func captureStreamOn(instance string) string { return StreamName(instance, captureSuffix) }

// publishCaptured stores one message on the capture stream the way the broker's MQTT
// gateway does: a plain publish on the device-events subject.
func publishCaptured(t *testing.T, js nats.JetStreamContext, body string) uint64 {
	t.Helper()
	ack, err := js.Publish(DeviceEventsSubject("test", "acme", "dev-1"), []byte(body))
	require.NoError(t, err)
	return ack.Sequence
}

func capturedHeld(t *testing.T, js nats.JetStreamContext) uint64 {
	t.Helper()
	info, err := js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	return info.State.Msgs
}

// GUARD: the stream is created as a work queue, and an ack removes the message from the
// stream, not just from the consumer.
func TestAnAckedCaptureMessageIsRemovedFromTheStream(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, readers := recorderRig(t, srv, uniqueArea("wq-ack"), recordNothing, captureSuffix)
	info, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)

	seq := publishCaptured(t, nmgr.js, "unread")
	require.EqualValues(t, 1, capturedHeld(t, nmgr.js), "an unread message is held")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := readers[0].ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, seq, msg.StreamSeq)
	require.EqualValues(t, 1, capturedHeld(t, nmgr.js), "a delivered but unacked message is held")
	require.NoError(t, msg.Ack())
	waitWithin(t, 3*time.Second, "the acked message to leave the stream", func() bool { return capturedHeld(t, nmgr.js) == 0 })
	_, err = nmgr.js.GetMsg(captureStreamOn("test"), seq)
	require.ErrorIs(t, err, nats.ErrMsgNotFound)
}

// GUARD: a work queue allows one consumer per subject filter. Nothing but the source's
// durable may attach to the capture stream, so a second durable on the same filter is
// refused by the broker rather than silently splitting the stream's messages.
func TestTheCaptureStreamRefusesASecondConsumer(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := recorderRig(t, srv, uniqueArea("wq-one"), recordNothing, captureSuffix)
	_, err := nmgr.js.AddConsumer(captureStreamOn("test"), &nats.ConsumerConfig{
		Durable: "another", AckPolicy: nats.AckExplicitPolicy,
		FilterSubject: DeviceEventsWildcard("test"),
	})
	require.Error(t, err, "a second consumer was attached to a work-queue stream with an overlapping filter")
}

// A give-up on the capture stream is lettered like any other, and the original is removed
// once the letter is written. The recorder used to skip every work-queue reader, which would
// have stopped lettering capture give-ups; and an unacked message on a work queue is kept
// until its age limit unless something removes it.
func TestACaptureGiveUpIsLetteredAndThenRemoved(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	area := uniqueArea("wq-giveup")
	nmgr, readers := recorderRig(t, srv, area, rec.build, captureSuffix)

	info, err := nmgr.js.ConsumerInfo(StreamName("test", streams.MaxDeliveries), DurableName("test", area, streams.MaxDeliveries))
	require.NoError(t, err)
	require.Contains(t, info.Config.FilterSubjects,
		AdvisorySubject(captureStreamOn("test"), CaptureDurableName("test", area, captureSuffix)),
		"the recorder does not listen for the capture durable's give-ups")

	seq := publishCaptured(t, nmgr.js, `{"n":1}`)
	for i := 1; i <= MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msg, err := readers[0].ReadMessage(ctx)
		cancel()
		require.NoError(t, err, "delivery %d never arrived", i)
		require.Equal(t, i, msg.NumDelivered)
	}
	keepPulling(t, readers[0])

	waitWithin(t, 10*time.Second, "the give-up to be recorded", func() bool { return len(rec.all()) == 1 })
	got := rec.all()[0]
	require.Equal(t, captureSuffix, got.Suffix)
	require.Equal(t, seq, got.StreamSeq)
	require.NotNil(t, got.Original)
	waitWithin(t, 5*time.Second, "the lettered original to leave the stream", func() bool { return capturedHeld(t, nmgr.js) == 0 })
	_, err = nmgr.js.GetMsg(captureStreamOn("test"), seq)
	require.ErrorIs(t, err, nats.ErrMsgNotFound)
	waitWithin(t, 3*time.Second, "the advisory to be acked", func() bool { return captureHeld(t, nmgr) == 0 })
}

// The original is removed only when the give-up was actually lettered. An outcome that wrote
// no letter leaves the message where it is, so nothing is dropped on the strength of a letter
// that does not exist.
func TestACaptureGiveUpWithoutALetterKeepsTheOriginal(t *testing.T) {
	for name, outcome := range map[string]MaxDeliveryOutcome{
		"unattributable": MaxDeliveryUnattributable,
		"lost":           MaxDeliveryLost,
	} {
		t.Run(name, func(t *testing.T) {
			srv := startEmbeddedServer(t)
			var seen atomic.Int32
			build := func(*NatsManager) (MaxDeliveryFunc, error) {
				return func(context.Context, MaxDelivery) (MaxDeliveryOutcome, error) {
					seen.Add(1)
					return outcome, nil
				}, nil
			}
			nmgr, readers := recorderRig(t, srv, uniqueArea("wq-noletter"), build, captureSuffix)
			seq := publishCaptured(t, nmgr.js, `{"n":1}`)
			for i := 1; i <= MaxDeliver; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := readers[0].ReadMessage(ctx)
				cancel()
				require.NoError(t, err)
			}
			keepPulling(t, readers[0])
			waitWithin(t, 10*time.Second, "the give-up to be handled", func() bool { return seen.Load() >= 1 })
			waitWithin(t, 3*time.Second, "the advisory to be acked", func() bool { return captureHeld(t, nmgr) == 0 })
			_, err := nmgr.js.GetMsg(captureStreamOn("test"), seq)
			require.NoError(t, err, "an original with no letter was removed")
		})
	}
}

// Out-of-order acks on a work queue leave holes behind the cursor and move the first
// sequence. None of that is a message a reader failed to read, so the unread-loss counter
// must not move; and an unread message the stream does evict is still counted.
func TestWorkQueueAcksAreNotUnreadLossButEvictionIs(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("wq-unread")
	ms := testMicroservice(t, srv, area)
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = 40
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	t.Cleanup(func() { nmgr.closeConn() })
	r, err := nmgr.NewReader(captureSuffix)
	require.NoError(t, err)
	g := &unreadRig{
		t: t, nmgr: nmgr, reg: reg, reader: r.(*natsReader),
		stream: captureStreamOn("test"), durable: CaptureDurableName("test", area, captureSuffix), area: area,
	}
	g.sample() // baseline

	// 30 messages delivered and acked in reverse, in three rounds, sampling between rounds.
	for round := 0; round < 3; round++ {
		for i := 0; i < 10; i++ {
			publishCaptured(t, nmgr.js, "m")
		}
		held := make([]Message, 0, 10)
		for i := 0; i < 10; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			m, err := g.reader.ReadMessage(ctx)
			cancel()
			require.NoError(t, err)
			held = append(held, m)
		}
		for i := len(held) - 1; i >= 0; i-- {
			require.NoError(t, held[i].Ack())
		}
		waitWithin(t, 5*time.Second, "the acked messages to leave the stream", func() bool { return capturedHeld(t, nmgr.js) == 0 })
		g.sample()
		require.Zero(t, g.mustSeries(skippedSeries), "acked work-queue messages were counted as unread loss (round %d)", round)
	}

	// 100 unread messages against a 40-message ceiling: the oldest 60 are evicted unread.
	for i := 0; i < 100; i++ {
		publishCaptured(t, nmgr.js, "m")
	}
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m, err := g.reader.ReadMessage(ctx)
		cancel()
		require.NoError(t, err)
		require.NoError(t, m.Ack())
	}
	g.sample()
	require.Positive(t, g.mustSeries(skippedSeries), "messages evicted before they were read were not counted")
}

// ---- upgrade: a stream created under the earlier retention ----

// migrationRig builds a started manager with a reader on the capture stream over a server
// whose capture stream was created beforehand under Limits retention, holding preloaded
// messages. The manager is returned before MigratePendingRetention is called.
func migrationRig(t *testing.T, srv *natsserver.Server, preload int) (*NatsManager, MessageReader) {
	t.Helper()
	var reader MessageReader
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("migrate")), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			r, err := n.NewReader(captureSuffix)
			reader = r
			return err
		})
	nmgr.RecordMaxDeliveries(recordNothing)
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name:      captureStreamOn("test"),
		Subjects:  StreamSubjects("test", captureSuffix),
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
	})
	require.NoError(t, err)
	for i := 0; i < preload; i++ {
		publishCaptured(t, nmgr.js, "preloaded")
	}
	nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = nmgr.Stop(stopCtx)
	})
	return nmgr, reader
}

// consumeCaptured reads and acks from reader until the test ends, returning the sequences it
// has seen so far on demand.
func consumeCaptured(t *testing.T, reader MessageReader) func() []uint64 {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var seen []uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			m, err := reader.ReadMessage(ctx)
			if err != nil {
				continue
			}
			mu.Lock()
			seen = append(seen, m.StreamSeq)
			mu.Unlock()
			_ = m.Ack()
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return func() []uint64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint64(nil), seen...)
	}
}

// An instance whose capture stream predates the work queue: the unread backlog is forwarded,
// then the stream is deleted and recreated as a work queue. The sequence carries on, because
// the capture sequence is the basis of the dedup id on the forwarded event, and the reader
// carries on without a restart.
func TestAnOlderCaptureStreamIsDrainedThenRecreated(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reader := migrationRig(t, srv, 3)

	require.Equal(t, []string{captureSuffix}, nmgr.PendingRetentionMigrations())
	info, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.LimitsPolicy, info.Config.Retention, "the stream must keep running as it is until it is drained")
	require.EqualValues(t, 3, info.State.Msgs)

	seen := consumeCaptured(t, reader)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, nmgr.MigratePendingRetention(ctx))
	require.Empty(t, nmgr.PendingRetentionMigrations())
	require.Len(t, seen(), 3, "the backlog was not forwarded before the stream was recreated")

	info, err = nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)
	require.Equal(t, StreamSubjects("test", captureSuffix), info.Config.Subjects)

	// The sequence carries on from the old stream's last, and the reader follows the stream.
	next := publishCaptured(t, nmgr.js, "after")
	require.Greater(t, next, uint64(1_000_003), "the recreated stream did not start past the old stream sequence plus the margin")
	waitWithin(t, 15*time.Second, "the reader to receive a message on the recreated stream", func() bool {
		s := seen()
		return len(s) == 4 && s[3] == next
	})
	waitWithin(t, 5*time.Second, "the acked message to leave the recreated stream", func() bool { return capturedHeld(t, nmgr.js) == 0 })
}

// A stream that is not drained within the bound is not touched, and the call says so.
func TestAnUndrainedOlderCaptureStreamIsLeftAloneAndRefused(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := migrationRig(t, srv, 2) // no one consumes
	nmgr.retentionDrainBound = 600 * time.Millisecond
	nmgr.retentionDrainPoll = 20 * time.Millisecond

	err := nmgr.MigratePendingRetention(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to start")

	info, serr := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, serr)
	require.Equal(t, nats.LimitsPolicy, info.Config.Retention, "an undrained stream was recreated")
	require.EqualValues(t, 2, info.State.Msgs, "unread messages were deleted")
	require.Equal(t, []string{captureSuffix}, nmgr.PendingRetentionMigrations())
}

// A stream with messages and no consumer to say they were read is not drained.
func TestAStreamHoldingMessagesWithNoConsumerIsNotDrained(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := migrationRig(t, srv, 1)
	require.NoError(t, nmgr.js.DeleteConsumer(captureStreamOn("test"), CaptureDurableName("test", nmgr.Microservice.FunctionalArea, captureSuffix)))
	nmgr.retentionDrainBound = 300 * time.Millisecond
	nmgr.retentionDrainPoll = 20 * time.Millisecond
	require.Error(t, nmgr.MigratePendingRetention(context.Background()))
}

// The refusal stays for everything but the one declared migration.
func TestOnlyTheDeclaredRetentionMigrationIsAllowed(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// The capture stream, under a retention that is not the declared old one.
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name:      captureStreamOn("test"),
		Subjects:  StreamSubjects("test", captureSuffix),
		Retention: nats.InterestPolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.ensureStream(captureSuffix)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention")
	require.Empty(t, nmgr.PendingRetentionMigrations())

	// A stream that is not declared recreatable, under the very pair of retentions the
	// declared migration moves between.
	_, err = nmgr.js.AddStream(&nats.StreamConfig{
		Name:      StreamName("test", streams.MaxDeliveries),
		Subjects:  StreamSubjects("test", streams.MaxDeliveries),
		Retention: nats.LimitsPolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.ensureStream(streams.MaxDeliveries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention")
	require.Empty(t, nmgr.PendingRetentionMigrations())

	// With nothing left to migrate the call is a no-op.
	require.NoError(t, nmgr.MigratePendingRetention(context.Background()))
}
