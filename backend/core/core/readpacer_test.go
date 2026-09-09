// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
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
