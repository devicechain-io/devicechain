// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// recordNothing is the max-delivery build for a test that starts readers but is not about
// recording: a manager with readers refuses to start without one.
func recordNothing(*NatsManager) (MaxDeliveryFunc, error) {
	return func(context.Context, MaxDelivery) (MaxDeliveryOutcome, error) {
		return MaxDeliveryLettered, nil
	}, nil
}

// capturedDeliveries is a max-delivery build that records what it was handed.
type capturedDeliveries struct {
	mu     sync.Mutex
	got    []MaxDelivery
	builds atomic.Int32
}

func (c *capturedDeliveries) build(*NatsManager) (MaxDeliveryFunc, error) {
	c.builds.Add(1)
	return func(_ context.Context, d MaxDelivery) (MaxDeliveryOutcome, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.got = append(c.got, d)
		return MaxDeliveryLettered, nil
	}, nil
}

func (c *capturedDeliveries) all() []MaxDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// recorderRig starts a manager for area the way a service does — NewNatsManager, a recorder,
// Initialize, Start — with one reader per suffix and a one-second AckWait.
func recorderRig(t *testing.T, srv *natsserver.Server, area string,
	build func(*NatsManager) (MaxDeliveryFunc, error), suffixes ...string) (*NatsManager, []MessageReader) {
	t.Helper()
	var readers []MessageReader
	nmgr := NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			for _, s := range suffixes {
				r, err := n.NewReader(s)
				if err != nil {
					return err
				}
				readers = append(readers, r)
			}
			return nil
		})
	if build != nil {
		nmgr.RecordMaxDeliveries(build)
	}
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = nmgr.Stop(stopCtx)
	})
	return nmgr, readers
}

// exhaust publishes one message on suffix for tenant acme and reads it MaxDeliver times
// without acking — every delivery abandoned, as a pod killed mid-handling abandons it. It
// returns the message's stream sequence.
func exhaust(t *testing.T, nmgr *NatsManager, reader MessageReader, suffix string) uint64 {
	t.Helper()
	ack, err := nmgr.js.Publish(ScopedSubject(nmgr.Microservice.InstanceId, "acme", suffix), []byte(`{"n":1}`))
	require.NoError(t, err)
	for i := 1; i <= MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msg, err := reader.ReadMessage(ctx)
		cancel()
		require.NoError(t, err, "delivery %d never arrived", i)
		require.Equal(t, i, msg.NumDelivered)
	}
	return ack.Sequence
}

// keepPulling keeps a puller on reader's durable until the test ends, as a live replica
// would. The broker sends its max-delivery advisory on the next PULL after the final
// delivery's ack window, so a test that stopped reading would wait on a broker that never
// speaks.
func keepPulling(t *testing.T, reader MessageReader) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if m, err := reader.ReadMessage(ctx); err == nil {
				_ = m.Ack()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// captureHeld is how many advisories the capture stream holds: under its work-queue
// retention, the ones no recorder has acked.
func captureHeld(t *testing.T, nmgr *NatsManager) uint64 {
	t.Helper()
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.MaxDeliveries))
	require.NoError(t, err)
	return info.State.Msgs
}

func waitWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
		time.Sleep(50 * time.Millisecond)
	}
}

// GUARD: pins the corrected premise that the whole recorder is built on. The advisory is NOT
// sent from the ack-window timer: after the final delivery expires, nothing is produced until
// the durable is pulled again. A recorder designed around a timer would promise a record the
// broker is not making; one designed around the pull is late while an area is down, never
// wrong.
func TestAdvisoryWaitsForTheNextPull(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("next-pull"), rec.build, streams.RaiseAlarm)
	seq := exhaust(t, nmgr, readers[0], streams.RaiseAlarm)

	// No puller: three whole ack windows and nothing.
	time.Sleep(3 * time.Second)
	require.Empty(t, rec.all(), "the recorder was handed an advisory nobody pulled for")
	require.EqualValues(t, 0, captureHeld(t, nmgr), "the broker produced the advisory without a pull")

	keepPulling(t, readers[0])
	waitWithin(t, 6*time.Second, "the advisory after a pull", func() bool { return len(rec.all()) == 1 })
	d := rec.all()[0]
	require.Equal(t, streams.RaiseAlarm, d.Suffix)
	require.Equal(t, StreamName("test", streams.RaiseAlarm), d.Stream)
	require.Equal(t, DurableName("test", nmgr.Microservice.FunctionalArea, streams.RaiseAlarm), d.Consumer)
	require.Equal(t, seq, d.StreamSeq)
	require.EqualValues(t, MaxDeliver, d.Deliveries)
	require.NotNil(t, d.Original, "the original was still on its stream")
	require.Equal(t, []byte(`{"n":1}`), d.Original.Data)
	require.False(t, d.At.IsZero())
	require.False(t, d.Final, "the capture durable's first delivery of the advisory is not its last")
	// A recorded advisory is acked, and a work queue deletes what is acked.
	waitWithin(t, 3*time.Second, "the capture stream to empty", func() bool { return captureHeld(t, nmgr) == 0 })
	require.Equal(t, 1.0, testutil.ToFloat64(nmgr.metrics.maxDeliveryRecords.WithLabelValues(d.Stream, "lettered")))
	require.Equal(t, 0.0, testutil.ToFloat64(nmgr.metrics.maxDeliveryRecords.WithLabelValues(d.Stream, "lost")),
		"the other outcomes exist at zero")
}

// GUARD: the capture is what makes a record LATE rather than LOST while the recorder is down.
// The advisory waits on the work queue, and a recorder that comes back letters it once.
func TestCapturedAdvisoryOutlivesTheRecorder(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("outlives"), rec.build, streams.RaiseAlarm)
	nmgr.stopRecorder(context.Background())

	exhaust(t, nmgr, readers[0], streams.RaiseAlarm)
	keepPulling(t, readers[0])
	waitWithin(t, 6*time.Second, "the advisory to be captured", func() bool { return captureHeld(t, nmgr) == 1 })
	require.Empty(t, rec.all(), "a stopped recorder handled an advisory")

	require.NoError(t, nmgr.startRecorder(context.Background()))
	waitWithin(t, 6*time.Second, "the restarted recorder to letter it", func() bool { return len(rec.all()) == 1 })
	waitWithin(t, 3*time.Second, "the capture stream to empty", func() bool { return captureHeld(t, nmgr) == 0 })
	time.Sleep(1500 * time.Millisecond)
	require.Len(t, rec.all(), 1, "the advisory was recorded more than once")
}

// GUARD: an original deleted after its advisory was captured — aged out, evicted or purged
// while the recorder was down — reaches the record func as a nil Original, and the advisory
// is still acked. (Deleted BEFORE the advisory, it is never advised at all: the broker drops a
// missing message from its pending set on the next pull.)
func TestAnOriginalDeletedAfterCaptureReachesTheRecordAsGone(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("gone"), rec.build, streams.RaiseAlarm)
	nmgr.stopRecorder(context.Background())
	seq := exhaust(t, nmgr, readers[0], streams.RaiseAlarm)
	keepPulling(t, readers[0])
	waitWithin(t, 6*time.Second, "the advisory to be captured", func() bool { return captureHeld(t, nmgr) == 1 })
	require.NoError(t, nmgr.js.DeleteMsg(StreamName("test", streams.RaiseAlarm), seq))

	require.NoError(t, nmgr.startRecorder(context.Background()))
	waitWithin(t, 6*time.Second, "the record", func() bool { return len(rec.all()) == 1 })
	require.Nil(t, rec.all()[0].Original)
	require.Equal(t, seq, rec.all()[0].StreamSeq)
	waitWithin(t, 3*time.Second, "the capture stream to empty", func() bool { return captureHeld(t, nmgr) == 0 })
}

// GUARD: two areas reading one stream each record only their own durables. The capture is a
// work queue, so an advisory taken by the wrong area's recorder is gone from the right one's.
func TestEachAreaRecordsOnlyItsOwnDurables(t *testing.T) {
	srv := startEmbeddedServer(t)
	recA, recB := &capturedDeliveries{}, &capturedDeliveries{}
	areaA, areaB := uniqueArea("area-a"), uniqueArea("area-b")
	nmgrA, _ := recorderRig(t, srv, areaA, recA.build, streams.RaiseAlarm)
	nmgrB, readersB := recorderRig(t, srv, areaB, recB.build, streams.RaiseAlarm)

	exhaust(t, nmgrB, readersB[0], streams.RaiseAlarm)
	keepPulling(t, readersB[0])
	waitWithin(t, 6*time.Second, "area b's record", func() bool { return len(recB.all()) == 1 })
	require.Equal(t, DurableName("test", areaB, streams.RaiseAlarm), recB.all()[0].Consumer)

	time.Sleep(1500 * time.Millisecond)
	require.Empty(t, recA.all(), "area a recorded area b's durable")
	info, err := nmgrA.js.ConsumerInfo(StreamName("test", streams.MaxDeliveries),
		DurableName("test", areaA, streams.MaxDeliveries))
	require.NoError(t, err)
	require.EqualValues(t, 0, info.Delivered.Consumer, "area a's capture durable was delivered an advisory")
}

// GUARD: kills "trust AddConsumer". A release that adds a reader must move the recorder's
// filter onto it; AddConsumer against the existing durable reports success and keeps the old
// list, so that reader's advisories would sit on the work queue with nobody to take them.
func TestRecorderFilterFollowsReaders(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("follows")
	first, _ := recorderRig(t, srv, area, recordNothing, streams.RaiseAlarm)
	captureStream := StreamName("test", streams.MaxDeliveries)
	durable := DurableName("test", area, streams.MaxDeliveries)
	info, err := first.js.ConsumerInfo(captureStream, durable)
	require.NoError(t, err)
	require.Equal(t, []string{AdvisorySubject(StreamName("test", streams.RaiseAlarm),
		DurableName("test", area, streams.RaiseAlarm))}, info.Config.FilterSubjects)
	require.NoError(t, first.Stop(context.Background()))

	second, _ := recorderRig(t, srv, area, recordNothing, streams.RaiseAlarm, streams.AlarmEvents)
	info, err = second.js.ConsumerInfo(captureStream, durable)
	require.NoError(t, err)
	want := []string{
		AdvisorySubject(StreamName("test", streams.RaiseAlarm), DurableName("test", area, streams.RaiseAlarm)),
		AdvisorySubject(StreamName("test", streams.AlarmEvents), DurableName("test", area, streams.AlarmEvents)),
	}
	got := slices.Clone(info.Config.FilterSubjects)
	slices.Sort(got)
	slices.Sort(want)
	require.Equal(t, want, got)
}

// GUARD: a manager with readers and no recorder refuses to start — the omission is a startup
// failure, never a silent gap. The counterweight: a manager with no readers (a publish-only
// service) needs none and starts no capture durable.
func TestReadersWithoutARecorderRefuseToStart(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("no-recorder")), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			_, err := n.NewReader(streams.RaiseAlarm)
			return err
		})
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	err := nmgr.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "RecordMaxDeliveries")
	t.Cleanup(func() { nmgr.nc.Close() })

	area := uniqueArea("publish-only")
	writerOnly := NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			_, err := n.NewWriter(streams.RaiseAlarm)
			return err
		})
	require.NoError(t, writerOnly.Initialize(ctx))
	require.NoError(t, writerOnly.Start(ctx))
	t.Cleanup(func() { _ = writerOnly.Stop(ctx) })
	require.Nil(t, writerOnly.recorderCancel)
	_, err = writerOnly.js.ConsumerInfo(StreamName("test", streams.MaxDeliveries),
		DurableName("test", area, streams.MaxDeliveries))
	require.Error(t, err, "a manager with no readers created a capture durable")
}

// GUARD: a start retried without a stop — the sequence a failed Starter postprocess leaves —
// keeps the one recorder it has instead of starting a second loop over the same durable.
func TestRetriedStartRunsOneRecorder(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, _ := recorderRig(t, srv, uniqueArea("retried"), rec.build, streams.RaiseAlarm)
	first := nmgr.recorder
	require.NotNil(t, first)
	require.NoError(t, nmgr.ExecuteStart(context.Background()))
	require.EqualValues(t, 1, rec.builds.Load(), "a retried start built a second recorder")
	require.Same(t, first, nmgr.recorder)
}

// GUARD: retention cannot be reconciled in place, and it decides what an ack means. A stream
// carrying the wrong one is refused rather than run on with semantics nobody declared — in
// both directions.
func TestExistingStreamWithWrongRetentionIsRefused(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name:      StreamName("test", streams.MaxDeliveries),
		Subjects:  StreamSubjects("test", streams.MaxDeliveries),
		Retention: nats.LimitsPolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.ensureStream(streams.MaxDeliveries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention")

	_, err = nmgr.js.AddStream(&nats.StreamConfig{
		Name:      StreamName("test", streams.RaiseAlarm),
		Subjects:  StreamSubjects("test", streams.RaiseAlarm),
		Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.ensureStream(streams.RaiseAlarm)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention")

	// The counterweight: a stream created by ensureStream itself is accepted on the next pass.
	_, err = nmgr.ensureStream(streams.AlarmEvents)
	require.NoError(t, err)
	_, err = nmgr.ensureStream(streams.AlarmEvents)
	require.NoError(t, err)
}

// A forged or garbled message on an advisory subject is counted as malformed and acked, and
// never reaches the record func: it is not a statement about any message of ours.
func TestAMalformedAdvisoryIsCountedAndNotRecorded(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	area := uniqueArea("malformed")
	nmgr, _ := recorderRig(t, srv, area, rec.build, streams.RaiseAlarm)
	stream := StreamName("test", streams.RaiseAlarm)
	subject := AdvisorySubject(stream, DurableName("test", area, streams.RaiseAlarm))
	_, err := nmgr.js.Publish(subject, []byte(`{"type":"io.nats.jetstream.advisory.v1.nak","stream_seq":1}`))
	require.NoError(t, err)
	waitWithin(t, 5*time.Second, "the malformed count", func() bool {
		return testutil.ToFloat64(nmgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "malformed")) == 1
	})
	waitWithin(t, 3*time.Second, "the capture stream to empty", func() bool { return captureHeld(t, nmgr) == 0 })
	require.Empty(t, rec.all())
}

// StreamSubjects gives the capture one subject per other declared stream, and nothing that
// could match another instance's streams; StreamSubject refuses it outright.
func TestTheCaptureListsEveryOtherStreamOfItsInstance(t *testing.T) {
	subjects := StreamSubjects("inst", streams.MaxDeliveries)
	require.Len(t, subjects, len(streams.All)-1)
	for _, s := range streams.All {
		if s.Suffix == streams.MaxDeliveries {
			continue
		}
		require.Contains(t, subjects, "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES."+StreamName("inst", s.Suffix)+".*")
	}
	for _, s := range subjects {
		require.True(t, strings.HasPrefix(s, "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.inst_"), s)
	}
	require.Panics(t, func() { StreamSubject("inst", streams.MaxDeliveries) })
	_, err := (&NatsManager{Microservice: &core.Microservice{InstanceId: "inst"}}).NewReader(streams.MaxDeliveries)
	require.Error(t, err)
	stream, durable, ok := parseAdvisorySubject(AdvisorySubject("inst_raise-alarm", "inst_area_raise-alarm"))
	require.True(t, ok)
	require.Equal(t, "inst_raise-alarm", stream)
	require.Equal(t, "inst_area_raise-alarm", durable)
	_, _, ok = parseAdvisorySubject("$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.a.b.c")
	require.False(t, ok)
}

// GUARD: kills "Final is never true". The record func is told, on each delivery of an
// advisory, whether it is the capture durable's LAST: that is the one delivery on which an
// error is no longer retried, so the func must turn a failure into a counted loss. Here the
// func fails on every delivery it is not told is final — so the advisory is redelivered
// MaxDeliver times, Final is false on the first MaxDeliver-1 and true on the MaxDeliver-th,
// and the "lost" it returns then is counted and the advisory acked off the work queue.
func TestTheCaptureDurablesLastDeliveryIsFinal(t *testing.T) {
	srv := startEmbeddedServer(t)
	var mu sync.Mutex
	var finals []bool
	build := func(*NatsManager) (MaxDeliveryFunc, error) {
		return func(_ context.Context, d MaxDelivery) (MaxDeliveryOutcome, error) {
			mu.Lock()
			defer mu.Unlock()
			finals = append(finals, d.Final)
			if !d.Final {
				return "", errors.New("the dead-letter stream is refusing writes")
			}
			return MaxDeliveryLost, nil
		}, nil
	}
	seen := func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(finals)
	}
	nmgr, readers := recorderRig(t, srv, uniqueArea("final"), build, streams.RaiseAlarm)
	exhaust(t, nmgr, readers[0], streams.RaiseAlarm)
	keepPulling(t, readers[0])

	waitWithin(t, 20*time.Second, "every delivery of the advisory", func() bool {
		return len(seen()) >= MaxDeliver
	})
	want := make([]bool, MaxDeliver)
	want[MaxDeliver-1] = true
	require.Equal(t, want, seen(), "Final must be false on every delivery but the MaxDeliver-th")
	waitWithin(t, 3*time.Second, "the capture stream to empty", func() bool { return captureHeld(t, nmgr) == 0 })
	stream := StreamName("test", streams.RaiseAlarm)
	require.Equal(t, 1.0, testutil.ToFloat64(nmgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "lost")))
	time.Sleep(1500 * time.Millisecond)
	require.Len(t, seen(), MaxDeliver, "the advisory was delivered again after its final delivery was acked")
}

// GUARD: kills "ExecuteStop does not stop the recorder". Stop must join the recorder — before
// the readers unsubscribe and the connection drains, since it may be mid-letter — and leave
// recorderCancel nil. A non-nil one is the retry guard's "a recorder is already running", so
// the next start would silently skip building one.
//
// A Stop followed by a Start on the SAME manager cannot be driven here: Stop drains the
// connection and nothing re-establishes it (see TestOncreateRunsOnEveryStart), and the
// lifecycle refuses a start from Stopped. What is asserted instead is the state that start's
// guard reads, and that the recorder goroutine has exited rather than running on into the drain.
func TestStopJoinsTheRecorder(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := recorderRig(t, srv, uniqueArea("stop"), recordNothing, streams.RaiseAlarm)
	require.NotNil(t, nmgr.recorderCancel)
	require.NoError(t, nmgr.Stop(context.Background()))
	require.Nil(t, nmgr.recorderCancel, "a later start would take the stopped recorder for a running one")
	require.Nil(t, nmgr.recorder)
	joined := make(chan struct{})
	go func() {
		nmgr.recorderWg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("the recorder goroutine was still running after Stop returned")
	}
}
