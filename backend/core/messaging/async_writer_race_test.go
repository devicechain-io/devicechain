// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The AsyncWriter's fan-in, partial failure, exactly-once outcome and shutdown, driven by
// hand. These run before async_writer_test.go's broker tests on purpose: a fan-in that lets
// two submissions into the orderedWriter at once makes the real writer PANIC, which kills
// the test binary wherever it happens. The first test here catches that shape by value, as
// an assertion, over a double that does not panic.

// ---- the fan-in itself, over a recording double ----

// recordingSubmitter stands in for the orderedWriter behind the fan-in and records what the
// fan-in does to it: the order submissions arrive in, and how many were inside it at once.
// Each submission dwells a little and yields, so an overlap that can happen does.
type recordingSubmitter struct {
	inside, most atomic.Int32

	mu  sync.Mutex
	got []string
}

func (r *recordingSubmitter) enter() {
	n := r.inside.Add(1)
	for {
		m := r.most.Load()
		if n <= m || r.most.CompareAndSwap(m, n) {
			break
		}
	}
	runtime.Gosched()
	time.Sleep(20 * time.Microsecond)
}

func (r *recordingSubmitter) publishGuarded(_ context.Context, msg Message, done func(error), guard func() error) {
	r.enter()
	if guard != nil {
		if err := guard(); err != nil {
			r.inside.Add(-1)
			done(err)
			return
		}
	}
	r.mu.Lock()
	r.got = append(r.got, string(msg.Value))
	r.mu.Unlock()
	r.inside.Add(-1)
	done(nil)
}

func (r *recordingSubmitter) Fail(err error, done func(error)) {
	r.enter()
	r.inside.Add(-1)
	done(err)
}

func (r *recordingSubmitter) Draining() {}
func (r *recordingSubmitter) Close()    {}

// 🔑 ONE SUBMISSION AT A TIME, EACH GOROUTINE'S IN ITS ORDER. Eight goroutines submit at
// once, with empty calls and two-message calls among the single ones. The writer behind the
// fan-in must never have two submissions inside it at once, must receive each goroutine's
// messages in that goroutine's order, and must run each goroutine's dones in that order.
//
// GUARD: kills a fan-in that submits from more than one goroutine (a goroutine per call, or
// callers submitting directly) by the in-flight count, and one that reorders a goroutine's
// calls by the order: both as assertions, where the real orderedWriter could report the
// first only by panicking.
func TestAsyncWriterFanInSubmitsOneAtATimeInEachGoroutinesOrder(t *testing.T) {
	const goroutines, calls = 8, 100
	rec := &recordingSubmitter{}
	w := newAsyncWriter(&NatsManager{}, streams.ResolvedEvents, rec, false)

	shape := func(g, i int) []Message {
		switch {
		case i%7 == 3: // empty: settles in its place, publishes nothing
			return nil
		case i%5 == 0:
			return []Message{{Value: []byte(fmt.Sprintf("%d:%d:a", g, i))}, {Value: []byte(fmt.Sprintf("%d:%d:b", g, i))}}
		default:
			return []Message{{Value: []byte(fmt.Sprintf("%d:%d:a", g, i))}}
		}
	}
	var mu sync.Mutex
	doneOrder := map[int][]int{}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < calls; i++ {
				w.Submit(orderedCtx(), shape(g, i), func(err error) {
					if err != nil {
						t.Errorf("call %d/%d: %v", g, i, err)
					}
					mu.Lock()
					doneOrder[g] = append(doneOrder[g], i)
					mu.Unlock()
				})
			}
		}(g)
	}
	wg.Wait()
	if err := w.Settle(boundedCtx(t)); err != nil {
		t.Fatalf("settle: %v", err)
	}
	w.Close()

	if most := rec.most.Load(); most != 1 {
		t.Fatalf("the writer behind the fan-in had %d submissions inside it at once, want 1: "+
			"the fan-in is not its single submitter", most)
	}
	want := 0
	for i := 0; i < calls; i++ {
		want += len(shape(0, i))
	}
	if len(rec.got) != goroutines*want {
		t.Fatalf("the writer received %d messages, want %d", len(rec.got), goroutines*want)
	}
	last := map[int]string{}
	for _, v := range rec.got {
		parts := strings.Split(v, ":")
		g, _ := strconv.Atoi(parts[0])
		i, _ := strconv.Atoi(parts[1])
		key := fmt.Sprintf("%06d:%s", i, parts[2])
		if prev, ok := last[g]; ok && key <= prev {
			t.Fatalf("goroutine %d: %q reached the writer after %q", g, v, prev)
		}
		last[g] = key
	}
	for g := 0; g < goroutines; g++ {
		if len(doneOrder[g]) != calls {
			t.Fatalf("goroutine %d: %d dones, want %d", g, len(doneOrder[g]), calls)
		}
		for k, i := range doneOrder[g] {
			if i != k {
				t.Fatalf("goroutine %d: done #%d was for call %d: not in call order", g, k, i)
			}
		}
	}
}

// ---- over a hand-driven JetStream context ----

// heldJS holds every publish until the test resolves it, and records what was sent.
type heldJS struct {
	nats.JetStreamContext

	mu   sync.Mutex
	futs []*fakeFuture
	vals []string
}

func (h *heldJS) PublishMsgAsync(m *nats.Msg, _ ...nats.PubOpt) (nats.PubAckFuture, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := &fakeFuture{ok: make(chan *nats.PubAck, 1), err: make(chan error, 1)}
	h.futs = append(h.futs, f)
	h.vals = append(h.vals, string(m.Data))
	return f, nil
}

func (h *heldJS) sent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.futs)
}

func (h *heldJS) sentValues() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.vals...)
}

// resolve answers publish i: a PubAck when err is nil, else err.
func (h *heldJS) resolve(i int, err error) {
	h.mu.Lock()
	f := h.futs[i]
	h.mu.Unlock()
	if err != nil {
		f.err <- err
		return
	}
	f.ok <- &nats.PubAck{Stream: "s", Sequence: uint64(i + 1)}
}

// waitSent waits up to asyncWithin for want publishes, failing the test if they never come.
func (h *heldJS) waitSent(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(asyncWithin)
	for h.sent() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%d publishes were sent, want %d", h.sent(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// heldAsync is an async writer over a heldJS, with an OPEN inbound-events gate the test can
// close, and stream metrics in a registry of its own.
func heldAsync(t *testing.T, window int, refuse bool) (*asyncWriter, *heldJS, *NatsManager, *gateState, *testClock) {
	t.Helper()
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	nmgr.Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)
	setGate(nmgr, gs, func() { gs.closed, gs.sampledAt = false, clk.now() })
	js := &heldJS{}
	ow := handWrittenWriter(window, js)
	ow.nmgr, ow.suffix = nmgr, streams.InboundEvents
	return newAsyncWriter(nmgr, streams.InboundEvents, ow, refuse), js, nmgr, gs, clk
}

// onceDone is a done callback that counts its calls and keeps the first error.
type onceDone struct {
	calls atomic.Int32
	mu    sync.Mutex
	err   error
	ran   chan struct{}
}

func newOnceDone() *onceDone { return &onceDone{ran: make(chan struct{}, 16)} }

func (d *onceDone) done(err error) {
	if d.calls.Add(1) == 1 {
		d.mu.Lock()
		d.err = err
		d.mu.Unlock()
	}
	d.ran <- struct{}{}
}

// wait waits for the first call and returns its error, failing the test after within.
func (d *onceDone) wait(t *testing.T, what string, within time.Duration) error {
	t.Helper()
	select {
	case <-d.ran:
	case <-time.After(within):
		t.Fatalf("%s: done did not run within %s", what, within)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// 🔑 A FAILED PIECE STOPS THE PIECES OF ITS CALL THAT HAVE NOT BEEN SENT. With a window of
// 1 the second and third pieces wait for room behind the first; when the first fails they
// are not published at all, and the call reports the first piece's own error, once. The
// writer then carries on.
//
// GUARD: kills "every piece is published after a failure" (the broker gets 3), a failure
// never recorded for the call, a guard that ignores it, a guard consulted before the piece
// has its slot (it would see no failure yet), and a call that reports the internal
// not-sent outcome instead of the real error.
func TestAsyncWriterAFailedPieceStopsTheCallsUnsentPieces(t *testing.T) {
	w, js, _, _, _ := heldAsync(t, 1, false)
	w.Draining() // the subject is the call, not the backoff
	t.Cleanup(w.Close)

	d := newOnceDone()
	w.Submit(orderedCtx(), []Message{{Value: []byte("1")}, {Value: []byte("2")}, {Value: []byte("3")}}, d.done)
	js.waitSent(t, 1)
	broke := errors.New("piece 1 refused by the broker")
	js.resolve(0, broke)
	if err := d.wait(t, "a call whose first piece failed", asyncWithin); !errors.Is(err, broke) {
		t.Fatalf("the call reported %v, want the first piece's own error", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := d.calls.Load(); n != 1 {
		t.Fatalf("done ran %d times for one call", n)
	}
	if got := js.sentValues(); len(got) != 1 {
		t.Fatalf("the broker was sent %v after the first piece failed, want only [1]", got)
	}

	next := newOnceDone()
	w.Submit(orderedCtx(), []Message{{Value: []byte("next")}}, next.done)
	js.waitSent(t, 2)
	js.resolve(1, nil)
	if err := next.wait(t, "the call after a failed one", asyncWithin); err != nil {
		t.Fatalf("the call after a failed one: %v", err)
	}
}

// The gate is read when the first piece is about to be SENT, not when the call is taken: a
// call that waited for room while the gate closed is refused, publishes nothing, and is
// counted once per message in jetstream_publish_refused_total.
//
// GUARD: kills a refusal decided before the wait for room (the call would go out through a
// closed gate), and a refusal counted per call instead of per message.
func TestAsyncWriterReadsTheGateWhenTheCallIsSent(t *testing.T) {
	w, js, nmgr, gs, clk := heldAsync(t, 1, true)
	w.Draining()
	t.Cleanup(w.Close)

	first := newOnceDone()
	w.Submit(orderedCtx(), []Message{{Value: []byte("a")}}, first.done)
	js.waitSent(t, 1)
	second := newOnceDone()
	// Returns once the writer has taken it; the writer then waits for room behind "a".
	w.Submit(orderedCtx(), []Message{{Value: []byte("b")}, {Value: []byte("c")}}, second.done)
	time.Sleep(50 * time.Millisecond)
	setGate(nmgr, gs, func() { gs.closed, gs.worst, gs.ratio, gs.sampledAt = true, "d", 0.97, clk.now() })
	js.resolve(0, nil)

	if err := first.wait(t, "the call sent while the gate was open", asyncWithin); err != nil {
		t.Fatalf("the call sent while the gate was open: %v", err)
	}
	if err := second.wait(t, "the call that waited while the gate closed", asyncWithin); !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a call that reached the send after the gate closed: %v, want ErrStreamBackpressure", err)
	}
	if got := js.sentValues(); len(got) != 1 {
		t.Fatalf("published %v, want only [a]", got)
	}
	stream := StreamName(nmgr.Microservice.InstanceId, streams.InboundEvents)
	if got := testutil.ToFloat64(nmgr.metrics.publishRefused.WithLabelValues(stream)); got != 2 {
		t.Fatalf("jetstream_publish_refused_total = %v, want 2 (one per message of the refused call)", got)
	}
}

// The tenant is checked before the gate, as MessageWriter does: a call with no tenant at a
// closed gate fails as the tenant error it is, not as a retryable refusal.
func TestAsyncWriterReportsAMissingTenantBeforeTheGate(t *testing.T) {
	w, js := gatedAsync(t, true, true)
	if err := submitAndWait(t, w, Message{Value: []byte("x")}); !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("control: a tenant's call at a closed gate = %v, want ErrStreamBackpressure", err)
	}
	d := newOnceDone()
	w.Submit(context.Background(), []Message{{Value: []byte("x")}}, d.done)
	if err := d.wait(t, "a call with no tenant", asyncWithin); err == nil || errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a call with no tenant at a closed gate = %v, want the tenant error", err)
	}
	if n := js.published.Load(); n != 0 {
		t.Fatalf("published %d", n)
	}
}

// 🔑 EVERY CALL GETS EXACTLY ONE OUTCOME, AND A CALLER WAITING FOR ROOM IS NEVER STRANDED.
// With a window of 1: "a" is in flight, "b" is taken and waits for room, "c" waits to be
// taken. Cancelling c's context returns it at once with its ctx error. "d" waits to be
// taken; Close (itself waiting on "a") releases it at once with ErrAsyncWriterClosed, and a
// Settle made then returns the same at once. Answering "a" lets "b" go; Close then returns,
// and no done runs after that.
//
// GUARD: kills a Submit that ignores its context while it waits, a Close that does not
// release waiting callers (or waits for them), a Settle that waits on a closed writer, and
// any path that runs a done twice or never.
func TestAsyncWriterEveryWaitingCallerGetsExactlyOneOutcome(t *testing.T) {
	w, js, _, _, _ := heldAsync(t, 1, false)
	a, b, c, d := newOnceDone(), newOnceDone(), newOnceDone(), newOnceDone()

	w.Submit(orderedCtx(), []Message{{Value: []byte("a")}}, a.done)
	js.waitSent(t, 1)
	w.Submit(orderedCtx(), []Message{{Value: []byte("b")}}, b.done)

	cctx, cancel := context.WithCancel(orderedCtx())
	defer cancel()
	cReturned := make(chan struct{})
	go func() { defer close(cReturned); w.Submit(cctx, []Message{{Value: []byte("c")}}, c.done) }()
	select {
	case <-cReturned:
		t.Fatal("c returned while the writer was busy and its context live")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-cReturned:
	case <-time.After(time.Second):
		t.Fatal("a Submit waiting for room ignored its cancelled context")
	}
	if err := c.wait(t, "c", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("c reported %v, want context.Canceled", err)
	}

	dReturned := make(chan struct{})
	go func() { defer close(dReturned); w.Submit(orderedCtx(), []Message{{Value: []byte("d")}}, d.done) }()
	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() { defer close(closed); w.Close() }()
	select {
	case <-dReturned:
	case <-time.After(time.Second):
		t.Fatal("Close did not release a Submit waiting to be taken")
	}
	if err := d.wait(t, "d", time.Second); !errors.Is(err, ErrAsyncWriterClosed) {
		t.Fatalf("d reported %v, want ErrAsyncWriterClosed", err)
	}
	sctx, scancel := context.WithTimeout(context.Background(), time.Second)
	defer scancel()
	if err := w.Settle(sctx); !errors.Is(err, ErrAsyncWriterClosed) {
		t.Fatalf("Settle on a closing writer = %v, want ErrAsyncWriterClosed at once", err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned with a publish unanswered")
	case <-time.After(100 * time.Millisecond):
	}

	js.resolve(0, nil)
	js.waitSent(t, 2)
	js.resolve(1, nil)
	select {
	case <-closed:
	case <-time.After(asyncWithin):
		t.Fatal("Close did not return once everything it held was answered")
	}
	if err := a.wait(t, "a", time.Second); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := b.wait(t, "b", time.Second); err != nil {
		t.Fatalf("b: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	for name, o := range map[string]*onceDone{"a": a, "b": b, "c": c, "d": d} {
		if n := o.calls.Load(); n != 1 {
			t.Fatalf("%s: done ran %d times, want exactly 1", name, n)
		}
	}
	if got := js.sentValues(); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("published %v, want [a b]", got)
	}
}

// The same exactly-once property under races nobody choreographs: sixteen goroutines submit
// with contexts that are already cancelled, cancelled while they wait, or live, while the
// writer is closed partway through. Every call reports once, and the calls reported as
// successes are exactly the ones published. Run it with -race.
func TestAsyncWriterExactlyOnceDoneUnderCancelAndCloseRaces(t *testing.T) {
	const goroutines, calls = 16, 200
	for round := 0; round < 5; round++ {
		clk := newTestClock()
		nmgr, _ := gatedManager(t, clk)
		js := &recordingJS{}
		ow := handWrittenWriter(4, js)
		ow.nmgr, ow.suffix = nmgr, streams.ResolvedEvents
		w := newAsyncWriter(nmgr, streams.ResolvedEvents, ow, false)

		counts := make([]atomic.Int32, goroutines*calls)
		var ok atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < calls; i++ {
					idx := g*calls + i
					ctx, cancel := context.WithCancel(orderedCtx())
					switch idx % 3 {
					case 0:
						cancel()
					case 1:
						time.AfterFunc(time.Duration(idx%50)*time.Microsecond, cancel)
					}
					w.Submit(ctx, []Message{{Value: []byte("x")}}, func(err error) {
						if counts[idx].Add(1) == 1 && err == nil {
							ok.Add(1)
						}
					})
					cancel()
				}
			}(g)
		}
		time.Sleep(time.Duration(round) * time.Millisecond)
		w.Close()
		wg.Wait()
		time.Sleep(20 * time.Millisecond)
		for idx := range counts {
			if n := counts[idx].Load(); n != 1 {
				t.Fatalf("round %d: call %d: done ran %d times, want exactly 1", round, idx, n)
			}
		}
		if got, want := ok.Load(), js.published.Load(); got != want {
			t.Fatalf("round %d: %d calls reported success and %d were published", round, got, want)
		}
	}
}

// failingJS fails every publish at the send, as a broker with no stream leader does.
type failingJS struct{ nats.JetStreamContext }

func (failingJS) PublishMsgAsync(*nats.Msg, ...nats.PubOpt) (nats.PubAckFuture, error) {
	return nil, nats.ErrNoResponders
}

// Close ends failure backoff: nothing will be submitted after it, so there is no backlog for
// the backoff to protect, and a Close that sat one out would spend shutdown budget on nothing.
//
// GUARD: kills a Close that does not end the backoff (it waits the 500 ms first step).
func TestAsyncWriterCloseDoesNotWaitOutABackoff(t *testing.T) {
	clk := newTestClock()
	nmgr, _ := gatedManager(t, clk)
	ow := handWrittenWriter(4, failingJS{})
	ow.nmgr, ow.suffix = nmgr, streams.ResolvedEvents
	w := newAsyncWriter(nmgr, streams.ResolvedEvents, ow, false)
	d := newOnceDone()
	w.Submit(orderedCtx(), []Message{{Value: []byte("x")}}, d.done)
	if err := d.wait(t, "a publish the broker failed", asyncWithin); err == nil {
		t.Fatal("a failed publish reported success")
	}
	// The settle loop is now in the backoff that failure began.
	start := time.Now()
	w.Close()
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatalf("Close took %s: it waited out the failure backoff (%s)", took, orderedBackoffStart)
	}
}
