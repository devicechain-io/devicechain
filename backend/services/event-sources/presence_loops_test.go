// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingRunner parks its first pass until it is released — a reconcile that has stopped
// making progress, which before the split meant the canary stopped with it.
type blockingRunner struct {
	entered  chan struct{}
	release  chan struct{}
	runs     atomic.Int32
	inFlight atomic.Bool
}

func (b *blockingRunner) Run(ctx context.Context) error {
	b.runs.Add(1)
	b.inFlight.Store(true)
	defer b.inFlight.Store(false)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

// countingProber records every probe.
type countingProber struct{ probes atomic.Int32 }

func (c *countingProber) Probe(context.Context) error {
	c.probes.Add(1)
	return nil
}

// TestTheCanaryKeepsProbingWhileAReconcileIsWEDGED is the whole reason the two loops were
// separated.
//
// 🔴🔴 THEY USED TO BE TWO ARMS OF ONE SELECT, so a reconcile that blocked meant Probe was
// never called again — presence_canary_missed_total could not increment, and an instance
// whose presence had stopped being read looked exactly like a healthy idle one. The
// instrument died with its subject, and both read healthy.
//
// 🔴 IT DRIVES runPresenceLoops, NOT THE TWO LOOPS DIRECTLY, and that is not a detail.
// Calling runReconcileLoop and runCanaryLoop from the test would assert that two
// independent goroutines are independent — which is true by construction and says nothing
// about the code that starts them. Measured: with the loops driven directly, a mutation
// putting the canary back on the reconcile's select SURVIVED the whole suite. Testing the
// callee is not testing the caller.
//
// 🔑 THE CONTROL IS THE IN-FLIGHT ASSERTION, NOT THE PROBE COUNT. "Probe was called" passes
// trivially if the reconcile is not actually blocking, which would measure the fixture
// rather than the fix. So this asserts probes accumulated WHILE the reconcile pass was
// still inside Run — the two facts have to hold at the same time or the test proves
// nothing.
func TestTheCanaryKeepsProbingWhileAReconcileIsWEDGED(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &blockingRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	canary := &countingProber{}
	rt := &presenceRuntime{stopped: make(chan struct{})}

	// A reconcile interval long enough that only the FIRST (immediate) pass runs, so the
	// wedge is a wedge rather than a queue of passes.
	runPresenceLoops(ctx, rt, runner, canary, time.Hour, 5*time.Millisecond)

	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the reconcile pass never started")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if canary.probes.Load() >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	probes := canary.probes.Load()

	// Both halves, together: the pass must STILL be blocked, and probes must have happened
	// anyway. Either alone is satisfiable by a broken arrangement.
	if !runner.inFlight.Load() {
		t.Fatal("the reconcile pass finished on its own; this test measured nothing")
	}
	if probes < 3 {
		t.Fatalf("the canary probed %d times while a reconcile was wedged; it must keep probing", probes)
	}
	close(runner.release)
}

// TestShutdownWaitsForBOTHLoops is why rt.stopped is closed behind a WaitGroup rather
// than by whichever loop happens to end first.
//
// 🔑 stopBrokerPresence WAITS ON rt.stopped WITH A FIVE-SECOND CAP. Closing it from one
// loop would report shutdown complete while the other was still running — and the very
// next thing shutdown does is close the NATS connection both of them use.
func TestShutdownWaitsForBOTHLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	runner := &blockingRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	canary := &countingProber{}
	rt := &presenceRuntime{stopped: make(chan struct{})}

	runPresenceLoops(ctx, rt, runner, canary, time.Millisecond, time.Millisecond)
	<-runner.entered

	// The reconcile pass is parked inside Run. Cancelling must end BOTH loops, and
	// rt.stopped must stay open until the parked pass has actually returned.
	select {
	case <-rt.stopped:
		t.Fatal("shutdown was reported complete while a reconcile pass was still running")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-rt.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("a presence loop did not stop when its context was cancelled")
	}
}

// TestTheLoopsRunWithNoCanary keeps the nil case working: an instance whose canary could
// not subscribe still reconciles.
func TestTheLoopsRunWithNoCanary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &blockingRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	rt := &presenceRuntime{stopped: make(chan struct{})}

	runPresenceLoops(ctx, rt, runner, nil, time.Millisecond, time.Millisecond)

	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciliation must still run when there is no canary")
	}
	close(runner.release)
}
