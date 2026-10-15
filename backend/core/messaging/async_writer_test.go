// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
)

// The AsyncWriter over a real embedded broker, driven the way the ordered writer's own tests
// are: the stream is replaced by publishStub, a responder that holds EVERY publish until the
// test answers it, so what the broker holds at once is exactly what the writer has in flight.

const asyncWithin = 10 * time.Second

func newAsync(t *testing.T, nmgr *NatsManager, window int, refuse bool) AsyncWriter {
	t.Helper()
	w, err := nmgr.NewAsyncWriter(streams.ResolvedEvents, window, refuse)
	if err != nil {
		t.Fatalf("new async writer: %v", err)
	}
	return w
}

// answerFailure replies to request i with a PubAck error, as a broker that refused the write.
func (s *publishStub) answerFailure(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answeredAt[i] = time.Now()
	_ = s.reqs[i].Respond([]byte(`{"error":{"code":503,"err_code":10077,"description":"stub refused"}}`))
}

// payloads is every request's body, in arrival order.
func (s *publishStub) payloads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.reqs))
	for i, r := range s.reqs {
		out[i] = string(r.Data)
	}
	return out
}

// answerAll answers every request as it arrives until n have been answered, or fails the test
// after asyncWithin. It runs on its own goroutine; the returned func waits for it.
func answerAll(t *testing.T, s *publishStub, n int) (wait func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(asyncWithin * 3)
		for i := 0; i < n; i++ {
			for s.seen() <= i {
				if time.Now().After(deadline) {
					t.Errorf("only %d of %d publishes ever reached the broker", s.seen(), n)
					return
				}
				time.Sleep(time.Millisecond)
			}
			s.answer(i)
		}
	}()
	return func() {
		select {
		case <-done:
		case <-time.After(asyncWithin * 4):
			t.Fatalf("answering %d publishes did not finish", n)
		}
	}
}

// waitCountOrFail waits for out to hold want outcomes, naming what it was waiting for.
func waitCountOrFail(t *testing.T, out *outcomes, want int, what string) {
	t.Helper()
	if got := out.waitCount(want, asyncWithin); got != want {
		t.Fatalf("%s: %d outcomes after %s, want %d", what, got, asyncWithin, want)
	}
}

// 🔑 THE WINDOW IS SHARED BY EVERY SUBMITTER. Three goroutines submit against a window of 8
// and the broker holds exactly 8 at once: not 3 (one PubAck apiece, the synchronous shape,
// which the control below shows holding exactly the goroutine count), and not 24.
//
// GUARD: kills "the fan-in serialises on the PubAck" (broker holds 1) and "the window is not
// applied to the fan-in" (holds more than 8).
func TestAsyncWriterKeepsTheWindowInFlight(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 8, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				w.Submit(orderedCtx(), []Message{{Value: []byte(fmt.Sprintf("%d-%d", g, i))}}, out.done(g*100+i))
			}
		}(g)
	}
	if got := stub.waitSeen(8); got != 8 {
		t.Fatalf("publishes the broker holds at once with a window of 8 and 3 submitters: %d, want exactly 8", got)
	}
	wait := answerAll(t, stub, 60)
	wait()
	waitCountOrFail(t, out, 60, "after every publish was answered")
	wg.Wait()
	w.Close()
}

// The negative control for the test above: the synchronous writer driven from the same three
// goroutines holds exactly 3, so the count the test reads really is concurrency and not the
// stub's own behaviour.
func TestSynchronousWriterHoldsOnePerGoroutine(t *testing.T) {
	srv, nmgr := orderedManager(t)
	sw, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	stub := stubPublishes(t, srv, nmgr)
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			_ = sw.WriteMessages(orderedCtx(), Message{Value: []byte(strconv.Itoa(g))})
		}(g)
	}
	if got := stub.waitSeen(3); got != 3 {
		t.Fatalf("the synchronous writer held %d publishes with 3 goroutines, want 3", got)
	}
	for i := 0; i < 3; i++ {
		stub.answer(i)
	}
	wg.Wait()
}

// Four goroutines submit 100 tagged messages each, mixed with two-message batches, through a
// window of 16 that is answered as fast as it fills. Nothing is lost, nothing duplicated, and
// each goroutine's messages are in the stream, and their dones ran, in that goroutine's order.
//
// GUARD: kills a fan-in that reorders one goroutine's calls, drops a call, publishes one twice,
// or runs dones out of per-goroutine order.
func TestAsyncWriterKeepsPerSubmitterOrderLosesNothingDuplicatesNothing(t *testing.T) {
	const goroutines, calls = 4, 100
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 16, false)
	stub := stubPublishes(t, srv, nmgr)

	// Every call is 1 message, every 5th is 2: 120 per goroutine.
	perG := calls + calls/5
	wait := answerAll(t, stub, goroutines*perG)

	var mu sync.Mutex
	doneOrder := map[int][]int{}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < calls; i++ {
				i := i
				msgs := []Message{{Value: []byte(fmt.Sprintf("%d:%d:a", g, i))}}
				if i%5 == 0 {
					msgs = append(msgs, Message{Value: []byte(fmt.Sprintf("%d:%d:b", g, i))})
				}
				w.Submit(orderedCtx(), msgs, func(err error) {
					if err != nil {
						t.Errorf("submit %d/%d failed: %v", g, i, err)
					}
					mu.Lock()
					doneOrder[g] = append(doneOrder[g], i)
					mu.Unlock()
				})
			}
		}(g)
	}
	wg.Wait()
	settleCtx, cancel := context.WithTimeout(context.Background(), asyncWithin*3)
	defer cancel()
	if err := w.Settle(settleCtx); err != nil {
		t.Fatalf("settle: %v", err)
	}
	wait()

	got := stub.payloads()
	if len(got) != goroutines*perG {
		t.Fatalf("the broker received %d messages, want %d", len(got), goroutines*perG)
	}
	seen := map[string]bool{}
	last := map[int]int{}
	for _, p := range got {
		if seen[p] {
			t.Fatalf("message %q was published twice", p)
		}
		seen[p] = true
		parts := strings.Split(p, ":")
		g, _ := strconv.Atoi(parts[0])
		i, _ := strconv.Atoi(parts[1])
		prev, ok := last[g]
		if !ok {
			prev = -1
		}
		if i < prev {
			t.Fatalf("goroutine %d: call %d reached the stream after call %d", g, i, prev)
		}
		last[g] = i
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
	w.Close()
}

// ---- backpressure, over a fake JetStream context and a hand-driven gate ----

// recordingJS acknowledges every publish at once and counts them.
type recordingJS struct {
	nats.JetStreamContext
	published atomic.Int64
}

func (j *recordingJS) PublishMsgAsync(_ *nats.Msg, _ ...nats.PubOpt) (nats.PubAckFuture, error) {
	j.published.Add(1)
	f := &fakeFuture{ok: make(chan *nats.PubAck, 1), err: make(chan error, 1)}
	f.ok <- &nats.PubAck{Stream: "s", Sequence: 1}
	return f, nil
}

// gatedAsync is an async writer over a fake JetStream context and a manager whose
// inbound-events gate the test sets by hand. closedGate says whether that gate refuses.
func gatedAsync(t *testing.T, refuse, closedGate bool) (AsyncWriter, *recordingJS) {
	t.Helper()
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	setGate(nmgr, gs, func() { gs.closed, gs.worst, gs.ratio, gs.sampledAt = closedGate, "d", 0.97, clk.now() })
	js := &recordingJS{}
	ow := handWrittenWriter(4, js)
	ow.nmgr, ow.suffix = nmgr, streams.InboundEvents
	w := newAsyncWriter(nmgr, streams.InboundEvents, ow, refuse)
	t.Cleanup(w.Close)
	return w, js
}

func submitAndWait(t *testing.T, w AsyncWriter, msgs ...Message) error {
	t.Helper()
	res := make(chan error, 1)
	w.Submit(orderedCtx(), msgs, func(err error) { res <- err })
	select {
	case err := <-res:
		return err
	case <-time.After(asyncWithin):
		t.Fatalf("done was not called within %s", asyncWithin)
		return nil
	}
}

// Telemetry is refused whole at a closed gate and never published; a call made only of
// bypassing messages is admitted; a mixed call is refused whole; an open gate admits.
//
// GUARD: kills "never refuses", "refuses everything", "refuses per message" (a mixed call
// would publish its bypassing half) and "publishes and refuses".
func TestAsyncWriterRefusesTelemetryAtAClosedGate(t *testing.T) {
	w, js := gatedAsync(t, true, true)
	tele, pres := Message{Value: []byte("t")}, Message{Value: []byte("p"), BypassBackpressure: true}

	if err := submitAndWait(t, w, tele); !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("telemetry at a closed gate: err = %v, want ErrStreamBackpressure", err)
	}
	if err := submitAndWait(t, w, pres, tele); !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a mixed call at a closed gate: err = %v, want ErrStreamBackpressure", err)
	}
	if n := js.published.Load(); n != 0 {
		t.Fatalf("%d messages were published by refused calls, want 0", n)
	}
	if err := submitAndWait(t, w, pres, pres); err != nil {
		t.Fatalf("a bypassing call at a closed gate: err = %v, want nil", err)
	}
	if n := js.published.Load(); n != 2 {
		t.Fatalf("a bypassing call of 2 published %d, want 2", n)
	}
}

func TestAsyncWriterAdmitsTelemetryAtAnOpenGate(t *testing.T) {
	w, js := gatedAsync(t, true, false)
	if err := submitAndWait(t, w, Message{Value: []byte("t")}); err != nil {
		t.Fatalf("telemetry at an open gate: %v", err)
	}
	if n := js.published.Load(); n != 1 {
		t.Fatalf("published %d, want 1", n)
	}
}

// A writer built NOT to refuse publishes at a closed gate: the forwarding-hop rule.
func TestAsyncWriterThatDoesNotRefuseIgnoresTheGate(t *testing.T) {
	w, js := gatedAsync(t, false, true)
	if err := submitAndWait(t, w, Message{Value: []byte("t")}); err != nil {
		t.Fatalf("a non-refusing writer at a closed gate: %v", err)
	}
	if n := js.published.Load(); n != 1 {
		t.Fatalf("published %d, want 1", n)
	}
}

// A refusal is reported IN ORDER with the publishes around it, and never ahead of them.
func TestAsyncWriterReportsARefusalInOrder(t *testing.T) {
	clk := newTestClock()
	gnmgr, gs := gatedManager(t, clk)
	setGate(gnmgr, gs, func() { gs.closed, gs.sampledAt = true, clk.now() })
	js := &latencyJetStream{latency: 50 * time.Millisecond}
	ow := handWrittenWriter(4, js)
	ow.nmgr, ow.suffix = gnmgr, streams.InboundEvents
	w := newAsyncWriter(gnmgr, streams.InboundEvents, ow, true)
	defer w.Close()

	out := newOutcomes()
	w.Submit(orderedCtx(), []Message{{Value: []byte("a"), BypassBackpressure: true}}, out.done(0))
	w.Submit(orderedCtx(), []Message{{Value: []byte("b")}}, out.done(1))
	w.Submit(orderedCtx(), []Message{{Value: []byte("c"), BypassBackpressure: true}}, out.done(2))
	waitCountOrFail(t, out, 3, "three calls")
	if fmt.Sprint(out.order) != "[0 1 2]" {
		t.Fatalf("outcomes in order %v, want [0 1 2]", out.order)
	}
	if out.errs[0] != nil || out.errs[2] != nil || !errors.Is(out.errs[1], ErrStreamBackpressure) {
		t.Fatalf("errors = %v, want only the middle call refused", out.errs)
	}
}

// ---- failure ----

// A multi-message call reports once, after EVERY piece settled, carrying the first error; a
// failed PubAck is never reported as success.
//
// GUARD: kills "done fires on the first piece", "done fires on the last PubAck received rather
// than the last piece", "the error is dropped" and "nil whenever the last piece succeeded".
func TestAsyncWriterBatchDoneWaitsForEveryPubAckAndKeepsTheError(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 8, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	w.Submit(orderedCtx(), []Message{{Value: []byte("1")}, {Value: []byte("2")}, {Value: []byte("3")}}, out.done(0))
	if got := stub.waitSeen(3); got != 3 {
		t.Fatalf("broker holds %d of the 3 pieces", got)
	}
	stub.answer(0)
	stub.answerFailure(1)
	time.Sleep(300 * time.Millisecond)
	if out.count() != 0 {
		t.Fatalf("done ran with piece 3 still unanswered")
	}
	stub.answer(2)
	waitCountOrFail(t, out, 1, "after the last piece")
	if out.errs[0] == nil {
		t.Fatal("a call with a failed piece reported success")
	}
	w.Draining()
	w.Close()
}

// A failed PubAck on a single message reaches that submitter's done, and the writer carries
// on: the next submission is published and succeeds.
func TestAsyncWriterSurfacesAFailedPubAckAndRecovers(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 4, false)
	w.Draining() // no backoff: the test is about reporting, not pacing
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	w.Submit(orderedCtx(), []Message{{Value: []byte("bad")}}, out.done(0))
	stub.waitSeen(1)
	stub.answerFailure(0)
	w.Submit(orderedCtx(), []Message{{Value: []byte("good")}}, out.done(1))
	stub.waitSeen(2)
	stub.answer(1)
	waitCountOrFail(t, out, 2, "failure then success")
	if out.errs[0] == nil {
		t.Fatal("the failed PubAck was reported as success")
	}
	if out.errs[1] != nil {
		t.Fatalf("the publish after a failure failed: %v", out.errs[1])
	}
	w.Close()
}

// A publish with no tenant fails loudly through done.
func TestAsyncWriterReportsAMissingTenant(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 4, false)
	defer w.Close()
	res := make(chan error, 1)
	w.Submit(context.Background(), []Message{{Value: []byte("x")}}, func(err error) { res <- err })
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("a publish with no tenant succeeded")
		}
	case <-time.After(asyncWithin):
		t.Fatal("no outcome for a publish with no tenant")
	}
}

// ---- settle ----

// 🔑 SETTLE IS A BARRIER, NOT A CLOSE. It returns only after every in-flight publish resolved,
// and the writer publishes again afterwards, across several cycles.
//
// GUARD: kills "returns at once", "returns on the first PubAck", "closes the writer" (a later
// Submit would be refused) and "settles before the earlier dones ran".
func TestSettleIsABarrierNotAClose(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 8, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	total := 0
	for cycle := 0; cycle < 3; cycle++ {
		for i := 0; i < 5; i++ {
			w.Submit(orderedCtx(), []Message{{Value: []byte(fmt.Sprintf("%d-%d", cycle, i))}}, out.done(total+i))
		}
		if got := stub.waitSeen(total + 5); got != total+5 {
			t.Fatalf("cycle %d: broker holds %d, want %d", cycle, got, total+5)
		}
		settled := make(chan error, 1)
		go func() { settled <- w.Settle(context.Background()) }()
		select {
		case err := <-settled:
			t.Fatalf("cycle %d: Settle returned (%v) with 5 publishes unanswered", cycle, err)
		case <-time.After(300 * time.Millisecond):
		}
		for i := 0; i < 4; i++ {
			stub.answer(total + i)
		}
		select {
		case err := <-settled:
			t.Fatalf("cycle %d: Settle returned (%v) with 1 publish unanswered", cycle, err)
		case <-time.After(300 * time.Millisecond):
		}
		stub.answer(total + 4)
		select {
		case err := <-settled:
			if err != nil {
				t.Fatalf("cycle %d: Settle = %v", cycle, err)
			}
		case <-time.After(asyncWithin):
			t.Fatalf("cycle %d: Settle did not return within %s of the last PubAck", cycle, asyncWithin)
		}
		if got := out.count(); got != total+5 {
			t.Fatalf("cycle %d: Settle returned with %d dones run, want %d", cycle, got, total+5)
		}
		total += 5
	}
	w.Close()
}

// Settle respects its context: a cancelled Settle returns ctx.Err() while a publish is held,
// abandons nothing already submitted, and the writer settles normally afterwards.
func TestSettleRespectsContextCancellation(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 8, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	w.Submit(orderedCtx(), []Message{{Value: []byte("held")}}, out.done(0))
	stub.waitSeen(1)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- w.Settle(ctx) }()
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a Settle past its deadline returned %v, want DeadlineExceeded", err)
		}
	case <-time.After(asyncWithin):
		t.Fatalf("Settle ignored its context for %s", asyncWithin)
	}

	stub.answer(0)
	if err := w.Settle(context.Background()); err != nil {
		t.Fatalf("settle after a cancelled one: %v", err)
	}
	if out.count() != 1 || out.errs[0] != nil {
		t.Fatalf("the submitted publish was lost or failed after a cancelled Settle: %d, %v", out.count(), out.errs[0])
	}
	w.Close()
}

// Settle on a window that is FULL still honours its context (the marker queues behind the
// window).
func TestSettleOnAFullWindowRespectsContextCancellation(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 2, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()
	// 3 submissions: 2 in flight, the 3rd parks the fan-in goroutine on the window and the
	// submitting goroutine on the unbuffered channel only for the 4th.
	for i := 0; i < 3; i++ {
		w.Submit(orderedCtx(), []Message{{Value: []byte(strconv.Itoa(i))}}, out.done(i))
	}
	stub.waitSeen(2)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- w.Settle(ctx) }()
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Settle = %v, want DeadlineExceeded", err)
		}
	case <-time.After(asyncWithin):
		t.Fatalf("Settle on a full window ignored its context for %s", asyncWithin)
	}
	wait := answerAll(t, stub, 3)
	wait()
	waitCountOrFail(t, out, 3, "after answering")
	w.Close()
}

// ---- close ----

// Close settles what is held and then refuses: nothing is published after it returns, and
// a Submit or Settle after it fails loudly.
//
// GUARD: kills "Close returns before the held publishes settle", "Submit after Close
// publishes" and "Submit after Close is silently dropped".
func TestNothingIsPublishedAfterClose(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 8, false)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()
	for i := 0; i < 3; i++ {
		w.Submit(orderedCtx(), []Message{{Value: []byte(strconv.Itoa(i))}}, out.done(i))
	}
	stub.waitSeen(3)

	closed := make(chan struct{})
	go func() { defer close(closed); w.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned with 3 publishes unanswered")
	case <-time.After(300 * time.Millisecond):
	}
	for i := 0; i < 3; i++ {
		stub.answer(i)
	}
	select {
	case <-closed:
	case <-time.After(asyncWithin):
		t.Fatalf("Close did not return within %s of the last PubAck", asyncWithin)
	}
	if out.count() != 3 {
		t.Fatalf("Close returned with %d of 3 dones run", out.count())
	}

	late := make(chan error, 1)
	w.Submit(orderedCtx(), []Message{{Value: []byte("late")}}, func(err error) { late <- err })
	select {
	case err := <-late:
		if !errors.Is(err, ErrAsyncWriterClosed) {
			t.Fatalf("Submit after Close reported %v, want ErrAsyncWriterClosed", err)
		}
	case <-time.After(asyncWithin):
		t.Fatal("Submit after Close never reported an outcome: a silent drop")
	}
	if err := w.Settle(context.Background()); !errors.Is(err, ErrAsyncWriterClosed) {
		t.Fatalf("Settle after Close = %v, want ErrAsyncWriterClosed", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := stub.seen(); got != 3 {
		t.Fatalf("the broker saw %d publishes, want 3: something was published after Close", got)
	}
	w.Close() // idempotent
}

// An unused writer owns no goroutine, and Close on it returns.
func TestAnUnusedAsyncWriterClosesCleanly(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 4, false)
	done := make(chan struct{})
	go func() { defer close(done); w.Close() }()
	select {
	case <-done:
	case <-time.After(asyncWithin):
		t.Fatal("Close on an unused writer did not return")
	}
}

// ---- leak ----

// 🔑 ONE WRITER ACROSS MANY LEASE TERMS DOES NOT LEAK. Every writer holds a reply subscription
// until the connection closes; Settle-and-reuse keeps the connection's subscription count
// constant across 20 cycles, while the per-term alternative (a fresh writer per cycle) grows
// it by one per cycle, which the control proves the instrument can see.
//
// GUARD: kills a Settle that builds a fresh writer or JetStream context per call.
func TestSettleCyclesDoNotLeakReplySubscriptions(t *testing.T) {
	_, nmgr := orderedManager(t)
	ctx, cancel := context.WithTimeout(orderedCtx(), asyncWithin*3)
	defer cancel()
	one := func(w AsyncWriter) {
		t.Helper()
		res := make(chan error, 1)
		w.Submit(ctx, []Message{{Value: []byte("x")}}, func(err error) { res <- err })
		if err := w.Settle(ctx); err != nil {
			t.Fatalf("settle: %v", err)
		}
		select {
		case err := <-res:
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
		default:
			t.Fatal("Settle returned before the publish's done")
		}
	}

	// Control first: a fresh writer per term leaks one subscription each.
	base := nmgr.nc.NumSubscriptions()
	for i := 0; i < 5; i++ {
		w := newAsync(t, nmgr, 4, false)
		one(w)
		w.Close()
	}
	if grew := nmgr.nc.NumSubscriptions() - base; grew < 5 {
		t.Fatalf("control: 5 per-term writers grew the connection's subscriptions by %d, want at least 5; "+
			"the instrument cannot see a leak", grew)
	}

	w := newAsync(t, nmgr, 4, false)
	one(w) // the first publish creates this writer's one reply subscription
	steady := nmgr.nc.NumSubscriptions()
	for i := 0; i < 20; i++ {
		one(w)
	}
	if got := nmgr.nc.NumSubscriptions(); got != steady {
		t.Fatalf("20 settle cycles moved the subscription count from %d to %d", steady, got)
	}
	w.Close()
}

// Close while a Submit caller is blocked behind a full window waits for it, and does not
// deadlock: the fourth submitter is parked on the fan-in channel (the loop is parked on the
// window with the third), Close queues behind it, and answers release everything.
func TestCloseWithBlockedSubmittersDoesNotDeadlock(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newAsync(t, nmgr, 2, false)
	stub := stubPublishes(t, srv, nmgr)
	var returned atomic.Int64
	for g := 0; g < 4; g++ {
		go func(g int) {
			w.Submit(orderedCtx(), []Message{{Value: []byte(strconv.Itoa(g))}}, func(error) {})
			returned.Add(1)
		}(g)
	}
	stub.waitSeen(2)
	if got := returned.Load(); got != 3 {
		t.Fatalf("%d Submit calls returned with a window of 2 held, want 3 (one parked on the loop, one blocked)", got)
	}
	closed := make(chan struct{})
	go func() { defer close(closed); w.Close() }()
	wait := answerAll(t, stub, 4)
	select {
	case <-closed:
	case <-time.After(asyncWithin * 2):
		t.Fatal("Close deadlocked behind a blocked submitter")
	}
	wait()
}
