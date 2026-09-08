// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// 🔴 WHAT THESE TESTS ARE FOR. ExecuteStop used to end at `close(cproc.quit)`. Closing
// quit stops the NEXT pass from starting and says nothing at all about the one already
// running, so ExecuteStop returned while a background pass was still in flight. Its
// caller (beforeMicroserviceStopped) then went on to stop the broker and the RdbManager,
// whose Terminate closes the pool — so the abandoned pass was still issuing queries
// against a pool that had gone, from a service that had already reported a clean stop.
// For the delivery sweep that pass PUBLISHES PHYSICAL ACTUATIONS and holds the sweep's
// advisory lock, which was then held by a goroutine the lifecycle no longer tracked.
//
// 🔑 A TEST THAT ONLY ASSERTS ExecuteStop RETURNS nil CANNOT SEE ANY OF THIS — it passes
// just as happily against the version with no join at all, which is the version that had
// the defect. So each test here puts one pass IN FLIGHT, blocked on a channel it owns,
// and asserts ExecuteStop has NOT returned. Releasing the pass is the counterweight: a
// join that never completes would be a hung shutdown, not a fixed one.
//
// 🔑 THERE IS ONE TEST PER PASS, NOT ONE FOR THE PROCESSOR, AND EACH PASS HAS ITS OWN
// GATE. The WaitGroup is registered at four separate call sites, and a test that blocked
// on whichever pass happened to arrive first would stay green with three of those four
// deleted — any one surviving registration is enough to hold ExecuteStop. That is not a
// hypothetical: the first version of this file shared one counter across the three lock
// methods, and the two reconciler tests were in fact parking the STARTUP DELIVERY pass,
// which left "the hold ticker is never registered" and "the stranded ticker is never
// registered" both alive. A gate per pass is what makes each registration load-bearing.

// passGate parks one pass on a channel the test owns.
//
// parkOn is 1-based and counts THAT GATE'S OWN calls; zero means "never park, answer
// immediately", which is how the passes a test is not about are waved through.
type passGate struct {
	mu     sync.Mutex
	calls  int
	parkOn int

	entered chan struct{}
	release chan struct{}
}

func newPassGate(parkOn int) *passGate {
	return &passGate{parkOn: parkOn, entered: make(chan struct{}, 1), release: make(chan struct{})}
}

// take is the lock body: park the designated call inside the pass, and decline every
// call — parked or not — so nothing further in the pass runs.
func (g *passGate) take() (bool, error) {
	g.mu.Lock()
	g.calls++
	park := g.parkOn > 0 && g.calls == g.parkOn
	g.mu.Unlock()
	if !park {
		return false, nil
	}
	g.entered <- struct{}{}
	<-g.release
	return false, nil
}

// blockingLockApi is a CommandDeliveryApi with an independent gate per pass. The embedded
// interface is nil on purpose: no pass gets past its lock, so a call to any other method
// would be a test that is not measuring what it claims to.
type blockingLockApi struct {
	model.CommandDeliveryApi

	sweep    *passGate
	hold     *passGate
	stranded *passGate
}

func newBlockingLockApi(sweepParkOn, holdParkOn, strandedParkOn int) *blockingLockApi {
	return &blockingLockApi{
		sweep:    newPassGate(sweepParkOn),
		hold:     newPassGate(holdParkOn),
		stranded: newPassGate(strandedParkOn),
	}
}

func (a *blockingLockApi) TrySweepLock(context.Context, func() error) (bool, error) {
	return a.sweep.take()
}

func (a *blockingLockApi) TryReconcileLock(context.Context, func() error) (bool, error) {
	return a.hold.take()
}

func (a *blockingLockApi) TryStrandedLock(context.Context, func() error) (bool, error) {
	return a.stranded.take()
}

// startedProc builds a literal processor with an EOF response reader and runs the real
// Initialize/Start callbacks, so what the test drives is the lifecycle's own wiring
// rather than a reconstruction of it.
func startedProc(t *testing.T, api model.CommandDeliveryApi, tune func(*CommandDeliveryProcessor)) *CommandDeliveryProcessor {
	t.Helper()
	proc := procWith(api, &recordingWriter{})
	proc.CommandResponsesReader = eofReader{}
	if tune != nil {
		tune(proc)
	}
	if err := proc.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := proc.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}
	return proc
}

// assertStopWaits is the assertion all four cases share: with one pass parked at its own
// gate, ExecuteStop must not return; released, it must.
func assertStopWaits(t *testing.T, proc *CommandDeliveryProcessor, gate *passGate, pass string) {
	t.Helper()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the %s pass never started; this test cannot measure a join it never set up", pass)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- proc.ExecuteStop(context.Background()) }()

	// The window is enormous relative to the work — an unjoined ExecuteStop closes a
	// channel and returns in microseconds — so this is a widened race, not a tight one.
	select {
	case err := <-stopped:
		t.Fatalf("ExecuteStop returned (err=%v) while the %s pass was still running: the "+
			"service reports a clean stop and then closes its database pool underneath a "+
			"goroutine still using it", err, pass)
	case <-time.After(250 * time.Millisecond):
	}

	close(gate.release)

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("ExecuteStop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ExecuteStop did not return after the %s pass was released; the join has "+
			"turned a shutdown into a hang", pass)
	}
}

// The startup pass. It is not on a ticker — ExecuteStart runs one immediately, for
// deliver-on-reconnect — but it is the same delivery sweep, and it is the pass most
// likely to be in flight at shutdown during a rolling restart. The sweep ticker is left
// at its default cadence here so nothing else can be the pass that holds the wait.
func TestStopWaitsForTheStartupDeliveryPass(t *testing.T) {
	api := newBlockingLockApi(1, 0, 0)
	proc := startedProc(t, api, nil)
	assertStopWaits(t, proc, api.sweep, "startup delivery")
}

// The sweep ticker. Its gate parks the SECOND sweep-lock call, because the startup pass
// takes the first and is waved straight through — that is what separates this test from
// the one above.
func TestStopWaitsForTheDeliverySweepTicker(t *testing.T) {
	api := newBlockingLockApi(2, 0, 0)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.SweepInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.sweep, "delivery sweep")
}

// The hold reconciler, parked at its own lock. The sweep gate parks nothing, so the
// startup delivery pass returns immediately and the only thing that can hold ExecuteStop
// is the hold pass this test is about.
func TestStopWaitsForTheHoldReconcilePass(t *testing.T) {
	api := newBlockingLockApi(0, 1, 0)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.holdInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.hold, "hold reconcile")
}

// The stranded-SENT reconciler, same shape again.
func TestStopWaitsForTheStrandedReconcilePass(t *testing.T) {
	api := newBlockingLockApi(0, 0, 1)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.strandedInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.stranded, "stranded reconcile")
}

// 🔑 THE COUNTERWEIGHT TO ALL FOUR. Waiting is only correct while a shutdown with nothing
// in flight is still prompt: a join placed where a loop can never reach its Done would
// satisfy every test above by hanging, and this is what tells the two apart.
func TestStopIsPromptWhenNoPassIsRunning(t *testing.T) {
	proc := startedProc(t, &fakeApi{}, nil)

	stopped := make(chan error, 1)
	go func() { stopped <- proc.ExecuteStop(context.Background()) }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("ExecuteStop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteStop hung with no pass in flight; the join is waiting on a loop that " +
			"cannot exit")
	}
}

// 🔑 THE OTHER HALF OF "the loop stops": the two reconcile loops used to select on quit
// ALONE, so they went on ticking after the root context was cancelled and only stopped at
// close(quit). Microservice shutdown cancels the root context BEFORE it calls Stop, and
// that root context is the one ExecuteStart hands to every loop — so a loop that ignores
// it keeps sweeping through a window in which the rest of the process is already being
// torn down. This drives each loop directly, since ExecuteStop closes quit and would
// therefore stop even a loop that reads nothing else.
func TestEveryPeriodicLoopStopsOnContextCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*CommandDeliveryProcessor, context.Context)
		tune func(*CommandDeliveryProcessor)
	}{
		{"sweep", (*CommandDeliveryProcessor).runSweepTicker,
			func(p *CommandDeliveryProcessor) { p.SweepInterval = time.Hour }},
		{"hold", (*CommandDeliveryProcessor).runHoldReconcileTicker,
			func(p *CommandDeliveryProcessor) { p.holdInterval = time.Hour }},
		{"stranded", (*CommandDeliveryProcessor).runStrandedReconcileTicker,
			func(p *CommandDeliveryProcessor) { p.strandedInterval = time.Hour }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := procWith(&fakeApi{}, &recordingWriter{})
			tc.tune(proc)
			// quit is left nil deliberately: a nil channel never becomes ready, so the
			// only way out of the loop is the cancellation this test is about.
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.run(proc, ctx)
			}()
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("the %s loop ignored its cancelled context and kept ticking through "+
					"a shutdown the rest of the process had already begun", tc.name)
			}
		})
	}
}
