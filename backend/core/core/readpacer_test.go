// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 🔴 WHAT THIS FILE IS FOR. Seven consumer loops in this tree wrapped a read in a bare
// for{} and, on any error that was not io.EOF, logged it and read again with no pause. A
// persistent instantly-returning error therefore became a spin: a burned core and a log
// line per iteration. Pacing the retry fixes that half. The other half is that pacing
// ALONE leaves a permanently-failing loop spinning slowly forever, with the pod still
// reporting ready — so the pacer also has a ceiling, past which the loop ends and the
// process says so.
//
// 🔑 THE MEASUREMENT IS THE ITERATION COUNT, NOT THE ELAPSED TIME. A paced loop and an
// unpaced one produce exactly the same output (none), so nothing about the result tells
// them apart; how many times the loop asked does. Every test here drives the pacer on a
// virtual clock, so the numbers are the loop's, not the machine's.

var errRead = errors.New("a read error that never clears")

// runToStop drives a pacer as a read loop would when every read fails, and returns how
// many failures it took to stop. limit bounds the count so an unpaced pacer fails the
// assertion instead of hanging the test.
func runToStop(t *testing.T, ctx context.Context, p *ReadPacer, limit int) (iterations int, stopped bool) {
	t.Helper()
	for i := 1; i <= limit; i++ {
		if p.PauseAfterError(ctx, errRead) {
			return i, true
		}
	}
	return limit, false
}

func TestAnUnclearableReadErrorEndsTheLoopWithinTheBudget(t *testing.T) {
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock())

	iterations, stopped := runToStop(t, context.Background(), p, 100000)
	if !stopped {
		t.Fatalf("the pacer never stopped: it retried %d times against an error that never "+
			"clears, which is the loop reporting healthy while consuming nothing", iterations)
	}

	// The bound is arithmetic, not a guess: 200ms doubling to a 5s cap covers the first
	// ~11s in 6 pauses, and the remaining budget in 5s steps.
	const bound = 40
	if iterations > bound {
		t.Fatalf("the pacer took %d retries to give up on a permanently failing read; more "+
			"than %d means the backoff is not actually spacing them out", iterations, bound)
	}
	t.Logf("gave up after %d consecutive failed reads", iterations)
}

// 🔑 THE COUNTERWEIGHT TO THE BOUND. Stopping quickly is only correct while the pacer is
// also PAUSING; a pacer that gave up after two instant retries would satisfy the test
// above and would have fixed nothing. This pins that the pauses are real, that they grow,
// and that they stop growing at the cap.
func TestThePausesGrowAndThenLevelOffAtTheCap(t *testing.T) {
	var waits []time.Duration
	now, sleep := VirtualClock()
	p := NewReadPacer(nil, "test stream").UseClock(now, func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return sleep(ctx, d)
	})

	if _, stopped := runToStop(t, context.Background(), p, 100000); !stopped {
		t.Fatal("the pacer never stopped, so there is no run of pauses to measure")
	}
	if len(waits) < 8 {
		t.Fatalf("only %d pauses were taken before the pacer gave up; that is too few to show "+
			"a backoff at all", len(waits))
	}
	if waits[0] != readErrorBackoffBase {
		t.Fatalf("the first pause was %s, want the base pause %s", waits[0], readErrorBackoffBase)
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] < waits[i-1] {
			t.Fatalf("pause %d (%s) is shorter than pause %d (%s): the backoff is going backwards",
				i, waits[i], i-1, waits[i-1])
		}
		if waits[i] > readErrorBackoffMax {
			t.Fatalf("pause %d is %s, past the %s cap: a long outage would leave the loop "+
				"checking too rarely to pick up when the broker returns", i, waits[i], readErrorBackoffMax)
		}
	}
	if last := waits[len(waits)-1]; last != readErrorBackoffMax {
		t.Fatalf("the final pause was %s, want the %s cap: the doubling never reached it", last, readErrorBackoffMax)
	}
}

// 🔑 THE BUDGET BOUNDS AN UNBROKEN RUN, NOT A LIFETIME. Without this, a service that hits
// one transient error every so often and recovers from every one of them would still walk
// its way to a give-up and tear itself down for faults it had already survived.
func TestASuccessfulReadClearsTheRunOfFailures(t *testing.T) {
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock())
	ctx := context.Background()

	// Far more failures than the budget could ever absorb in one run, each followed by a
	// success — as a loop that keeps recovering would report.
	for i := 0; i < 5000; i++ {
		if p.PauseAfterError(ctx, errRead) {
			t.Fatalf("the pacer gave up at intermittent failure %d even though every one of "+
				"them was followed by a successful read", i+1)
		}
		p.Succeeded()
	}
}

// 🔑 THE PAUSE MUST NOT DELAY A SHUTDOWN. A loop parked in a five-second backoff still has
// to leave promptly when its context is cancelled, or every service's stop grows the
// backoff.
func TestACancelledContextStopsTheLoopInsteadOfPausing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock())
	if !p.PauseAfterError(ctx, errRead) {
		t.Fatal("the pacer told the loop to keep reading on a cancelled context; a shutdown " +
			"would then wait out the whole backoff")
	}
}

// 🔑 AND THE REAL SLEEP HAS TO HONOUR CANCELLATION TOO. Every other test here replaces the
// clock, so nothing else exercises the production wait at all — which would leave the one
// function that actually blocks the loop untested.
func TestTheProductionWaitReturnsOnCancellationRatherThanElapsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan bool, 1)
	go func() { done <- sleepUntilCancelled(ctx, time.Hour) }()

	select {
	case elapsed := <-done:
		if elapsed {
			t.Fatal("the wait reported that a full hour elapsed on an already-cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait ignored its cancelled context and is sitting out the full hour")
	}
}

// 🔴 THE REPORTING HALF, WHICH NOTHING EXERCISED UNTIL THIS BLOCK EXISTED. Every test
// above builds its pacer with NewReadPacer(nil, ...), so all of them measure a pacer that
// STOPS and none of them measure one that SAYS SO. That gap ran the whole depth of the
// tree: eleven loops had adopted this type, each one's tests pinning that it stops, and
// the line the whole design turns on — the give-up reaching the microservice — had never
// been executed by any test at any layer.
//
// 🔑 AND "STOPS" WITHOUT "SAYS SO" IS THE ORIGINAL DEFECT WEARING A DIFFERENT HAT. A loop
// that quietly stops is a pod that reports ready and consumes nothing — which is exactly
// the outcome the ceiling was added to remove. So a pacer whose report silently went
// nowhere would pass every other test in this file while delivering none of the value.

// recordingSink stands where the microservice stands. It has to: the production sink ends
// the process, so a test that let it run would take the test binary down with it — which
// is the reason this branch went untested for so long.
//
// It never blocks: the sink is called inline on the read goroutine (see reportTo).
type recordingSink struct {
	mu   sync.Mutex
	errs []error
}

func newRecordingSink() *recordingSink {
	return &recordingSink{}
}

func (r *recordingSink) report(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recordingSink) reported() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func TestAnExhaustedBudgetIsReportedAndNotOnlyLogged(t *testing.T) {
	sink := newRecordingSink()
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock()).reportTo(sink.report)

	if _, stopped := runToStop(t, context.Background(), p, 100000); !stopped {
		t.Fatal("the pacer never gave up, so there was nothing to report")
	}

	// The report is made inline on the read goroutine, so it has landed by the time the
	// loop has stopped.
	got := sink.reported()
	if len(got) == 0 {
		t.Fatal("the pacer gave up on the loop but reported it to nobody: the process keeps " +
			"running, the pod goes on reporting ready, and the only trace is a log line")
	}
	if len(got) != 1 {
		t.Fatalf("the give-up was reported %d times, want exactly 1: a repeated report races "+
			"several teardowns against each other: %v", len(got), got)
	}
	// The text is asserted because this error is the ONLY account an operator gets of why
	// the pod exited. A report that arrives without naming the stream or wrapping the
	// cause is a restart with no explanation attached.
	msg := got[0].Error()
	for _, want := range []string{"test stream", "consecutive reads", "not recovering"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the give-up error does not mention %q, so the restart it causes arrives "+
				"without that context: %s", want, msg)
		}
	}
	if !errors.Is(got[0], errRead) {
		t.Errorf("the give-up error does not wrap the read error that caused it, so the actual "+
			"broker fault is lost: %s", msg)
	}
}

// 🔴 A GIVE-UP REPORTED FROM THE READ GOROUTINE, THROUGH THE REAL FailNow. The report
// tears the process down, and teardown runs the component's ExecuteStop, which waits on
// the very read goroutine that is reporting. giveUp calls FailNow inline, which is safe
// only because FailNow returns at once and runs the teardown on its own goroutine (the
// contract failnow_async_test.go pins). Were it to run the teardown inline, the Stopper
// below would wait on a loop that is waiting on the Stopper: the budget would expire and
// the outcome would read "teardown did not finish within 1s" instead of naming the stream
// that stopped draining — the operator told the shutdown was slow rather than what broke.
func TestAGiveUpThroughTheRealFailNowReportsTheStreamNotTheBudget(t *testing.T) {
	captureExit(t)
	loopReturned := make(chan struct{})
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		<-loopReturned // the component's Stop joins its read loop
		return nil
	}
	ms := runnable(t, callbacks)
	oneSecondBudget(t, ms)
	p := NewReadPacer(ms, "test stream").UseClock(VirtualClock())

	go func() {
		defer close(loopReturned)
		runToStop(t, context.Background(), p, 100000)
	}()

	// Bounded, so a give-up that never reaches FailNow fails here with a message rather
	// than hanging until the package timeout panics.
	start := time.Now()
	outcome := make(chan error, 1)
	go func() { outcome <- ms.waitForShutdown() }()
	var err error
	select {
	case err = <-outcome:
	case <-time.After(10 * time.Second):
		t.Fatal("the give-up never ended the process: nothing reached FailNow within 10s")
	}
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("the give-up ended the process with a nil outcome, which exits 0")
	}
	msg := err.Error()
	for _, want := range []string{"test stream", "not recovering"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the outcome does not name %q, so the exit does not say what broke: %s", want, msg)
		}
	}
	if strings.Contains(msg, "teardown did not finish") {
		t.Errorf("the teardown waited on the read loop that reported: %s", msg)
	}
	if elapsed >= time.Second {
		t.Errorf("the shutdown took %s, the whole teardown budget: something waited on the read loop", elapsed)
	}
}

// 🔑 THE COUNTERWEIGHT. Both tests above are satisfied by a pacer that reports on every
// error, which would tear the process down for one broker hiccup. Nothing may be reported
// while the loop is recovering.
func TestARecoveringLoopIsNeverReported(t *testing.T) {
	sink := newRecordingSink()
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock()).reportTo(sink.report)

	for i := 0; i < 5000; i++ {
		if p.PauseAfterError(context.Background(), errRead) {
			t.Fatalf("the pacer gave up at intermittent failure %d even though every one was "+
				"followed by a successful read", i+1)
		}
		p.Succeeded()
	}
	if got := sink.reported(); len(got) != 0 {
		t.Fatalf("a loop that recovered from every failure was reported as unfit %d times; "+
			"the process would be torn down for faults it had already survived: %v",
			len(got), got)
	}
}

// 🔴 THE WIRING ITSELF, which is the half a sink-based test cannot see. Every test above
// installs its own sink, so all of them would keep passing if NewReadPacer stopped
// deriving one from the microservice — and then every production pacer would stop, log,
// and report to nobody, exactly as before this type existed.
//
// 🔑 IT GOES THROUGH THE REAL FailNow, AND AN EARLIER VERSION OF THIS TEST DID NOT. That
// version asserted only that the sink was non-nil, which a stub satisfies: wiring
// func(error){} in place of ms.FailNow passed it — and that mutant is WORSE than the
// defect this commit fixes, because the give-up then reaches neither the process nor the
// nil branch's log line and the loop stops in complete silence.
//
// Calling it is safe, which the earlier version's excuse got wrong: FailNow on a
// struct-literal Microservice takes the not-yet-started branch, records the outcome and
// returns. It does not exit — only Run does. struct_literal_test.go already catalogues
// FailNow that way, which is where this recipe comes from.
func TestAMicroserviceBackedPacerReportsThroughIt(t *testing.T) {
	ms := &Microservice{}
	p := NewReadPacer(ms, "test stream")
	if p.fail == nil {
		t.Fatal("a pacer built with a microservice has no report sink: every give-up in " +
			"production would be logged and dropped, and the pod would stay up")
	}

	p.fail(errors.New("the read loop is unfit"))

	// The outcome is what the process exits on, so this is what says the report reached the
	// thing that actually ends the pod.
	//
	// 🔑 RECEIVED WITH A DEADLINE RATHER THAN waitForShutdown's BARE BLOCKING RECEIVE. A
	// sink that reports nowhere — the no-op stub this test exists to kill — produces no
	// outcome at all, and the bare receive then parks until the whole package times out.
	// That is a failure that names nothing; this one names the defect.
	var err error
	select {
	case err = <-ms.outcomeCh():
	case <-time.After(5 * time.Second):
		t.Fatal("the give-up produced no outcome at all: the sink swallowed it, so the loop " +
			"stops in silence and the process keeps running as though nothing happened")
	}
	if err == nil {
		t.Fatal("the give-up left a nil outcome, so the process would exit 0 — " +
			"indistinguishable from an orderly stop, which is the reporting this type exists " +
			"to provide")
	}
	if !strings.Contains(err.Error(), "the read loop is unfit") {
		t.Errorf("the outcome does not carry the give-up's own error, so the exit would not "+
			"say what failed: %v", err)
	}
	// FailNow CLAIMS the process. A sink wired to something that merely records an outcome
	// — ms.finished, say — would satisfy the assertions above and leave the process
	// unclaimed, skipping the teardown entirely.
	if ph := ms.phase.Load(); ph != phaseStopping {
		t.Errorf("the microservice is in phase %d rather than phaseStopping: the report went "+
			"somewhere that does not actually shut the process down", ph)
	}

	if p := NewReadPacer(nil, "test stream"); p.fail != nil {
		t.Fatal("a pacer built without a microservice invented a sink; the documented nil " +
			"case is what lets processors be assembled by literal in their own tests")
	}
}
