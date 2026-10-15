// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// latencyJetStream is a JetStream context whose publishes are acknowledged a fixed time after
// they are sent, the way a replicated stream answers: late, but every one, and not slower for
// there being many. It counts how many are awaiting their acknowledgement, and the most there
// ever were.
type latencyJetStream struct {
	nats.JetStreamContext
	latency time.Duration

	inflight atomic.Int64
	peak     atomic.Int64
}

func (j *latencyJetStream) PublishMsgAsync(_ *nats.Msg, _ ...nats.PubOpt) (nats.PubAckFuture, error) {
	n := j.inflight.Add(1)
	for {
		p := j.peak.Load()
		if n <= p || j.peak.CompareAndSwap(p, n) {
			break
		}
	}
	f := &fakeFuture{ok: make(chan *nats.PubAck, 1), err: make(chan error, 1)}
	time.AfterFunc(j.latency, func() {
		j.inflight.Add(-1)
		f.ok <- &nats.PubAck{Stream: "s", Sequence: 1}
	})
	return f, nil
}

// handWrittenWriter is an ordered writer over a fake JetStream context and no broker.
func handWrittenWriter(window int, js nats.JetStreamContext) *orderedWriter {
	return &orderedWriter{
		nmgr:     &NatsManager{Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"}},
		suffix:   streams.ResolvedEvents,
		js:       js,
		slots:    make(chan struct{}, window),
		pending:  make(chan orderedPending, window),
		settled:  make(chan struct{}),
		draining: make(chan struct{}),
	}
}

// publishAllWithin bounds a run that a broken writer can stop dead: a slot that is never
// freed parks the submitter (or Close) for good, and a hang is not a failure.
const publishAllWithin = 30 * time.Second

// publishAll submits n messages, closes the writer, and returns the order the outcomes came
// back in and how long the whole run took. A run that does not finish within
// publishAllWithin fails the test instead of hanging it (the stuck goroutine is abandoned).
func publishAll(t *testing.T, w *orderedWriter, n int) ([]int, time.Duration) {
	t.Helper()
	var mu sync.Mutex
	var order []int
	var submitted atomic.Int64
	start := time.Now()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < n; i++ {
			i := i
			w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, func(err error) {
				if err != nil {
					t.Errorf("publish %d failed: %v", i, err)
				}
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
			})
			submitted.Add(1)
		}
		w.Close()
	}()
	select {
	case <-finished:
	case <-time.After(publishAllWithin):
		mu.Lock()
		settled := len(order)
		mu.Unlock()
		t.Fatalf("the writer stopped: %d of %d publishes submitted and %d settled after %s, "+
			"so a window slot was never freed (or never taken back) and the submitter or Close is parked",
			submitted.Load(), n, settled, publishAllWithin)
	}
	return order, time.Since(start)
}

// 🔑 THE LAW THE PUBLISH WINDOW RESTS ON: with a fixed acknowledgement latency, publishes per
// second are the window divided by that latency. A window of 128 at 10 ms completes 2,048
// publishes in 16 round trips (160 ms); 512 does it in 4 (40 ms). The window is also a
// HARD cap, never exceeded and always reached, and outcomes come back in submission order
// however deep it is.
//
// GUARD: kills "the window is ignored" (peak above it), "the window is smaller than asked"
// (peak below it), and a settle loop that reports out of order.
func TestOrderedWriterThroughputIsWindowOverLatency(t *testing.T) {
	const latency = 10 * time.Millisecond
	const total = 2048

	run := func(window int) time.Duration {
		js := &latencyJetStream{latency: latency}
		w := handWrittenWriter(window, js)
		order, took := publishAll(t, w, total)

		if got := js.peak.Load(); got != int64(window) {
			t.Errorf("window %d: the most publishes awaiting a PubAck was %d, want exactly %d", window, got, window)
		}
		if len(order) != total {
			t.Fatalf("window %d: %d outcomes, want %d", window, len(order), total)
		}
		for i, got := range order {
			if got != i {
				t.Fatalf("window %d: outcome %d was for publish %d: not in submission order", window, i, got)
			}
		}
		return took
	}

	narrow, wide := run(128), run(512)
	if narrow < 160*time.Millisecond {
		t.Errorf("window 128 at %s finished %d publishes in %s; Little's law says at least 160ms", latency, total, narrow)
	}
	if wide > 100*time.Millisecond {
		t.Errorf("window 512 at %s finished %d publishes in %s; it should take about 40ms", latency, total, wide)
	}
}

// The window the writer was built with is the one it reports. Two sizes, so an answer fixed
// at one of them cannot pass.
func TestOrderedWriterReportsItsWindow(t *testing.T) {
	_, nmgr := orderedManager(t)
	for _, want := range []int{300, MaxOrderedWindow} {
		w := newOrdered(t, nmgr, want)
		if got := w.(interface{ Window() int }).Window(); got != want {
			t.Errorf("Window() = %d, want %d", got, want)
		}
		w.Close()
	}
}

func windowFullSeconds(nmgr *NatsManager) float64 {
	return testutil.ToFloat64(nmgr.metrics.publishWindowFull.WithLabelValues(streams.ResolvedEvents))
}

func windowFullWaits(nmgr *NatsManager) float64 {
	return testutil.ToFloat64(nmgr.metrics.publishWindowFullWaits.WithLabelValues(streams.ResolvedEvents))
}

func duplicateCount(nmgr *NatsManager) float64 {
	return testutil.ToFloat64(nmgr.metrics.publishDuplicates.WithLabelValues(streams.ResolvedEvents))
}

// Time blocked on a full window is counted, and time with room in it is not.
//
// The waits are asserted as a COUNT as well as in seconds. A free slot is taken in well
// under a microsecond, which a coarse clock (Windows) reads as zero, so a writer that
// charged every submission would still read 0 seconds in the roomy case; it cannot read 0
// waits.
//
// GUARD: kills "never counted" (the blocked case reads 0), "counted on every publish" (the
// roomy case reads a wait), and a wait timed from after the slot was won (about 0 seconds).
func TestWindowFullTimeIsCountedOnlyWhileBlocked(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	w := newOrdered(t, nmgr, 2)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	w.Publish(orderedCtx(), Message{Value: []byte("0")}, out.done(0))
	w.Publish(orderedCtx(), Message{Value: []byte("1")}, out.done(1))
	if got, waits := windowFullSeconds(nmgr), windowFullWaits(nmgr); got != 0 || waits != 0 {
		t.Fatalf("two publishes into a window of two were charged %v waits and %vs", waits, got)
	}

	released := make(chan struct{})
	go func() {
		defer close(released)
		w.Publish(orderedCtx(), Message{Value: []byte("2")}, out.done(2))
	}()
	stub.waitSeen(2)
	time.Sleep(200 * time.Millisecond)
	select {
	case <-released:
		t.Fatal("a third publish into a full window of two was sent before any slot was freed")
	default:
	}
	stub.answer(0)
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("the third publish was still waiting 10s after the first PubAck freed a slot")
	}
	stub.waitSeen(3)
	stub.answer(1)
	stub.answer(2)
	closed := make(chan struct{})
	go func() { defer close(closed); w.Close() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return 10s after every publish was answered")
	}

	if got := windowFullSeconds(nmgr); got < 0.15 || got > 5 {
		t.Fatalf("a submitter held 200ms by a full window was charged %vs, want about 0.2", got)
	}
	if waits := windowFullWaits(nmgr); waits != 1 {
		t.Fatalf("one submitter held by a full window was counted as %v waits, want 1", waits)
	}
}

// A PubAck marked Duplicate is counted, one marked stored is not.
//
// GUARD: kills "the duplicate counter never incremented" and "every PubAck counted".
func TestDuplicatePubAcksAreCounted(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	w := newOrdered(t, nmgr, 8)
	out := newOutcomes()

	w.Publish(orderedCtx(), Message{Value: []byte("a"), DedupID: "id-1"}, out.done(0))
	w.Publish(orderedCtx(), Message{Value: []byte("b"), DedupID: "id-2"}, out.done(1))
	w.Draining()
	if out.waitCount(2, 10*time.Second) != 2 {
		t.Fatal("publishes were not settled")
	}
	if got := duplicateCount(nmgr); got != 0 {
		t.Fatalf("two distinct dedup ids counted %v duplicates", got)
	}

	w.Publish(orderedCtx(), Message{Value: []byte("a"), DedupID: "id-1"}, out.done(2))
	w.Publish(orderedCtx(), Message{Value: []byte("a"), DedupID: "id-1"}, out.done(3))
	if out.waitCount(4, 10*time.Second) != 4 {
		t.Fatal("republishes were not settled")
	}
	w.Close()
	if got := duplicateCount(nmgr); got != 2 {
		t.Fatalf("two republishes of a stored id counted %v duplicates, want 2", got)
	}
	for i := 0; i < 4; i++ {
		if out.errs[i] != nil {
			t.Fatalf("publish %d failed: %v", i, out.errs[i])
		}
	}
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Fatalf("stream holds %d messages, want 2 (the republishes are duplicates)", info.State.Msgs)
	}
}
