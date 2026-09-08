// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/messaging"
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
// GATE. The WaitGroup is registered at five separate call sites, and a test that blocked
// on whichever pass happened to arrive first would stay green with four of those five
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

	// parked is true while a call is held, and duringPark counts the calls that arrive
	// from OTHER goroutines while it is. See callsDuringPark.
	parked     bool
	duringPark int

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
	if park {
		g.parked = true
	} else if g.parked {
		g.duringPark++
	}
	g.mu.Unlock()
	if !park {
		return false, nil
	}
	g.entered <- struct{}{}
	<-g.release
	g.mu.Lock()
	g.parked = false
	g.mu.Unlock()
	return false, nil
}

// callsDuringPark is how many calls reached this gate from some other goroutine while one
// was parked. It is what lets a test PROVE which goroutine it parked — see
// TestStopWaitsForTheDeliverySweepTicker, whose whole difficulty is that the startup pass
// and the sweep ticker call the same method.
func (g *passGate) callsDuringPark() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.duringPark
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

// parkingReader holds the response-consumer loop inside ReadMessage, then ends it. EOF on
// release is what the real reader does on a cancelled context, so the loop unwinds the way
// it does in production rather than by some route invented here.
type parkingReader struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *parkingReader) ReadMessage(context.Context) (messaging.Message, error) {
	r.once.Do(func() {
		r.entered <- struct{}{}
		<-r.release
	})
	return messaging.Message{}, io.EOF
}
func (r *parkingReader) HandleResponse(error) {}

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

// assertStopWaits is the assertion every case shares: with one pass parked at its own
// gate, ExecuteStop must not return; released, it must.
//
// beforeStop, when given, runs after the pass is parked and BEFORE ExecuteStop is called.
// That ordering is not incidental: ExecuteStop closes quit before it waits, which stops
// every ticker, so a check placed inside the wait window would observe a system in which
// nothing can tick and would answer "quiet" no matter what it was asked. The first version
// of the attribution check below was written there and could not fail — its negative
// control passed, which is how it was caught.
func assertStopWaits(t *testing.T, proc *CommandDeliveryProcessor, entered <-chan struct{},
	release chan struct{}, pass string, beforeStop func()) {
	t.Helper()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the %s pass never started; this test cannot measure a join it never set up", pass)
	}

	if beforeStop != nil {
		beforeStop()
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

	close(release)

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
	assertStopWaits(t, proc, api.sweep.entered, api.sweep.release, "startup delivery", nil)
}

// The sweep ticker.
//
// 🔴 THIS IS THE ONE TEST WHOSE PASS IS NOT IDENTIFIED BY WHICH METHOD IT CALLS. The
// startup pass and the ticker both go through TrySweepLock, so parking "the second call"
// only reaches the ticker if the startup goroutine is scheduled within the tick interval.
// That assumption fails in the WORSE direction — under CI load the ticker takes call one,
// the startup pass parks at call two, and the test silently measures the startup
// registration instead, leaving "the sweep ticker is never registered" alive with a green
// suite.
//
// 🔑 SO THE ATTRIBUTION IS ASSERTED RATHER THAN ASSUMED, and callsDuringPark is what
// asserts it. With a call parked and the processor otherwise still running, one of two
// things is true: the parked goroutine is the TICKER, in which case the ticker cannot tick
// and — the startup pass having already been one of the two earlier calls — nothing else
// reaches this gate; or the parked goroutine is the STARTUP pass, in which case the ticker
// is free and lands a call every 5ms. Requiring zero over a window many ticks long
// therefore passes only when the parked pass really is the ticker, and the mis-scheduled
// case becomes a loud red instead of a vacuous green.
//
// 🔴 THE CHECK RUNS BEFORE ExecuteStop, AND IT IS WORTHLESS ANYWHERE ELSE. ExecuteStop
// closes quit before it waits, which stops the ticker — so the same check made while
// ExecuteStop was blocked would observe a system that cannot tick and would answer "zero"
// however wrong the attribution was. It was written there first, and its negative control
// (park call one, which is the startup pass) passed. This is the version that fails it.
func TestStopWaitsForTheDeliverySweepTicker(t *testing.T) {
	api := newBlockingLockApi(3, 0, 0)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.SweepInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.sweep.entered, api.sweep.release, "delivery sweep", func() {
		// Long enough for ~10 ticks, so a free ticker cannot go unnoticed.
		time.Sleep(50 * time.Millisecond)
		if n := api.sweep.callsDuringPark(); n != 0 {
			t.Fatalf("%d further sweep-lock calls arrived while the pass was parked, so the "+
				"parked pass was NOT the ticker (a ticking ticker is exactly what produces "+
				"them) and this test is measuring some other goroutine's registration", n)
		}
	})
}

// The hold reconciler, parked at its own lock. The sweep gate parks nothing, so the
// startup delivery pass returns immediately and the only thing that can hold ExecuteStop
// is the hold pass this test is about.
func TestStopWaitsForTheHoldReconcilePass(t *testing.T) {
	api := newBlockingLockApi(0, 1, 0)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.holdInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.hold.entered, api.hold.release, "hold reconcile", nil)
}

// The stranded-SENT reconciler, same shape again.
func TestStopWaitsForTheStrandedReconcilePass(t *testing.T) {
	api := newBlockingLockApi(0, 0, 1)
	proc := startedProc(t, api, func(p *CommandDeliveryProcessor) {
		p.strandedInterval = 5 * time.Millisecond
	})
	assertStopWaits(t, proc, api.stranded.entered, api.stranded.release, "stranded reconcile", nil)
}

// The inbound response-consumer loop. It is not a ticker and not a database pass, but it
// is a background goroutine ExecuteStart launches and it does write through the same pool,
// so leaving it untracked is the same shape as the four above. Its gate is the reader
// rather than the API, which is also what makes it unambiguous: no other goroutine reads
// messages.
func TestStopWaitsForTheResponseConsumer(t *testing.T) {
	reader := &parkingReader{entered: make(chan struct{}, 1), release: make(chan struct{})}
	proc := startedProc(t, &fakeApi{}, func(p *CommandDeliveryProcessor) {
		p.CommandResponsesReader = reader
	})
	assertStopWaits(t, proc, reader.entered, reader.release, "response consumer", nil)
}

// 🔑 THE COUNTERWEIGHT TO ALL FIVE. Waiting is only correct while a shutdown with nothing
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
