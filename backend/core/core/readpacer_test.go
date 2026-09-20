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
// tree: twelve loops had adopted this type, each one's tests pinning that it stops, and
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
type recordingSink struct {
	mu      sync.Mutex
	errs    []error
	entered chan struct{}
	release chan struct{}
}

func newRecordingSink() *recordingSink {
	return &recordingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (r *recordingSink) report(err error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
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
	close(sink.release) // this test does not need to hold the sink open
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock()).reportTo(sink.report)

	if _, stopped := runToStop(t, context.Background(), p, 100000); !stopped {
		t.Fatal("the pacer never gave up, so there was nothing to report")
	}

	// The report is dispatched on its own goroutine, so wait for it rather than racing it.
	deadline := time.After(5 * time.Second)
	for len(sink.reported()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the pacer gave up on the loop but reported it to nobody: the process keeps " +
				"running, the pod goes on reporting ready, and the only trace is a log line")
		case <-time.After(time.Millisecond):
		}
	}

	got := sink.reported()
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

// 🔴 THE SELF-DEADLOCK GUARD, AND IT IS THE REASON giveUp SPAWNS A GOROUTINE. The sink
// tears the process down, and teardown runs the component's ExecuteStop, which waits on
// the very read goroutine that is reporting. Called inline, the shutdown waits for a loop
// that is parked inside the shutdown, and the pod hangs until its grace period kills it —
// turning a clean non-zero exit into a SIGKILL, which is the reporting this type exists
// for, lost at the last step.
//
// Delete the `go` in giveUp and this test hangs and then fails; nothing else in this file
// notices.
func TestTheGiveUpDoesNotBlockTheLoopThatRaisedIt(t *testing.T) {
	sink := newRecordingSink() // release is NOT closed: the sink blocks, as a teardown would
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock()).reportTo(sink.report)

	returned := make(chan bool, 1)
	go func() {
		_, stopped := runToStop(t, context.Background(), p, 100000)
		returned <- stopped
	}()

	// The sink must actually have been entered, or this test would pass against a pacer
	// that never reported at all.
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was never called, so this test proves nothing about blocking")
	}

	select {
	case stopped := <-returned:
		if !stopped {
			t.Fatal("the loop returned without stopping")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop is still parked inside its own give-up: the report is being made " +
			"on the read goroutine, so teardown will wait for a loop that is waiting for " +
			"teardown and the pod hangs until it is SIGKILLed")
	}
	close(sink.release)
}

// 🔑 THE COUNTERWEIGHT. Both tests above are satisfied by a pacer that reports on every
// error, which would tear the process down for one broker hiccup. Nothing may be reported
// while the loop is recovering.
func TestARecoveringLoopIsNeverReported(t *testing.T) {
	sink := newRecordingSink()
	close(sink.release)
	p := NewReadPacer(nil, "test stream").UseClock(VirtualClock()).reportTo(sink.report)

	for i := 0; i < 5000; i++ {
		if p.PauseAfterError(context.Background(), errRead) {
			t.Fatalf("the pacer gave up at intermittent failure %d even though every one was "+
				"followed by a successful read", i+1)
		}
		p.Succeeded()
	}
	time.Sleep(10 * time.Millisecond) // give any errant goroutine time to land
	if got := sink.reported(); len(got) != 0 {
		t.Fatalf("a loop that recovered from every failure was reported as unfit %d times; "+
			"the process would be torn down for faults it had already survived: %v",
			len(got), got)
	}
}

// 🔴 THE WIRING ITSELF, which is the half a sink-based test cannot see. Every test above
// installs its own sink, so all of them would keep passing if NewReadPacer stopped
// deriving one from the microservice entirely — and then every production pacer would
// stop, log, and report to nobody, exactly as before this type existed.
//
// The sink is only INSPECTED here, never called: calling it would end the test process,
// which is the whole reason the seam above exists.
func TestAMicroserviceBackedPacerHasSomewhereToReport(t *testing.T) {
	if p := NewReadPacer(&Microservice{}, "test stream"); p.fail == nil {
		t.Fatal("a pacer built with a microservice has no report sink: every give-up in " +
			"production would be logged and dropped, and the pod would stay up")
	}
	if p := NewReadPacer(nil, "test stream"); p.fail != nil {
		t.Fatal("a pacer built without a microservice invented a sink; the documented nil " +
			"case is what lets processors be assembled by literal in their own tests")
	}
}
