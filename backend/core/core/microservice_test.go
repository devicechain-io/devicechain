// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureExit swaps the process-exit hook for the duration of one test and returns
// the codes it was asked to exit with. The stub RETURNS, which the real os.Exit never
// does, so a test also proves the statement after it is harmless — see reportOutcome.
//
// It replaces a package-level variable, so a test using it must not call t.Parallel().
// Nothing enforces that; it is stated because a parallel test would swap the hook out
// from under another one and read someone else's exit codes.
func captureExit(t *testing.T) *[]int {
	t.Helper()
	codes := []int{}
	prev := exitProcess
	exitProcess = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitProcess = prev })
	return &codes
}

// shutdownDrainDelay resolves the drain window from the environment, falling back
// to the default and tolerating invalid input.
func TestShutdownDrainDelay(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset uses default", false, "", defaultShutdownDrain},
		{"explicit seconds", true, "10", 10 * time.Second},
		{"zero disables drain", true, "0", 0},
		{"empty uses default", true, "", defaultShutdownDrain},
		{"invalid uses default", true, "soon", defaultShutdownDrain},
		{"negative uses default", true, "-3", defaultShutdownDrain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(ENV_SHUTDOWN_DRAIN_SECONDS, tc.val)
			}
			assert.Equal(t, tc.want, shutdownDrainDelay())
		})
	}
}

// A service that refuses to start must say so in its exit status. This is the whole
// point of the outcome channel: before it, Run returned nil here and the container
// reported Completed, which is what an orderly shutdown reports.
func TestRunExitsNonZeroWhenStartupIsRefused(t *testing.T) {
	codes := captureExit(t)

	// An invalid instance id is the earliest fail-closed guard in InitializeAndStart,
	// so this exercises the real Run path without needing a config volume or a broker.
	ms := &Microservice{InstanceId: "not a valid token", outcome: make(chan error, 1)}
	err := ms.Run()

	// The exit code is the production-observable assertion. The returned error is only
	// visible because captureExit's stub returns where os.Exit would not, so treat it
	// as a way to identify WHICH failure was reported, not as a caller contract.
	assert.Equal(t, []int{1}, *codes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid instance id")
}

// starting builds a Microservice as it is while still coming up: phase Starting, and a
// lifecycle in the state where a service spends most of its startup. Both helpers exist
// so no test hand-rolls the outcome channel — finished() sends on it, and on a
// zero-value Microservice that is a nil channel, which blocks forever inside the Once.
func starting(t *testing.T, callbacks LifecycleCallbacks) *Microservice {
	t.Helper()
	ms := &Microservice{outcome: make(chan error, 1)}
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.lifecycle = NewLifecycleManager("test", ms, callbacks)
	ms.lifecycle.State = Initializing
	return ms
}

// runnable builds a Microservice in the state a real one is in once startup has
// finished: phase Running, lifecycle Started, ready to be torn down.
func runnable(t *testing.T, callbacks LifecycleCallbacks) *Microservice {
	t.Helper()
	ms := starting(t, callbacks)
	ms.lifecycle.State = Started
	ms.phase.Store(phaseRunning)
	return ms
}

// 🔴 THE COUNTERWEIGHT. Reporting a failure is only useful while an orderly shutdown
// still exits 0 — a rolling update terminates every pod on purpose, so a false positive
// here paints every deployment red.
//
// It goes through ShutDownNow deliberately. An earlier version called finished(nil)
// directly, which skipped the drain, the cancel, Stop, Terminate AND the success send
// itself — so mutating that send to report a failure left this test green while every
// orderly shutdown in the fleet exited 1. A counterweight that does not traverse the
// path it is weighing is not one.
func TestAnOrderlyShutdownExitsZero(t *testing.T) {
	codes := captureExit(t)

	ms := runnable(t, NewNoOpLifecycleCallbacks())
	ms.ShutDownNow()

	assert.NoError(t, ms.reportOutcome(ms.waitForShutdown()))
	assert.Empty(t, *codes, "an orderly shutdown must not set an exit status")
}

// A teardown that failed is a failure too, end to end — and this covers the WIRING,
// not just the helper. ShutDownNow is the only thing that reports one, and its sends
// sit within a dozen lines of each other: failures and one success. A test that only
// exercised finished() would pass with all of them reporting the same thing.
func TestAFailedTeardownIsReported(t *testing.T) {
	codes := captureExit(t)

	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		return errors.New("could not close the inbound reader")
	}
	ms := runnable(t, callbacks)

	ms.ShutDownNow()

	err := ms.reportOutcome(ms.waitForShutdown())
	require.Error(t, err, "a shutdown that could not stop the service must not report success")
	assert.Contains(t, err.Error(), "could not close the inbound reader")
	assert.Equal(t, []int{1}, *codes)
}

// Terminate reports on its own line, a dozen lines from Stop's. Covering only Stop left
// this one free to be flipped to report success with the suite green — which a mutation
// run confirmed before this test existed.
func TestAFailedTerminateIsReported(t *testing.T) {
	codes := captureExit(t)

	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Terminator.Preprocess = func(context.Context) error {
		return errors.New("could not close the database pool")
	}
	ms := runnable(t, callbacks)

	ms.ShutDownNow()

	err := ms.reportOutcome(ms.waitForShutdown())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not close the database pool")
	assert.Equal(t, []int{1}, *codes)
}

// 🔴 THE REGRESSION TEST. A SIGTERM landing during initialization used to run the full
// teardown against a service that did not exist yet — Stop's guards permit a stop from
// Initializing — which in 11 of the 15 services dereferences package values that
// initialization had not assigned yet. Where it did not panic it SUCCEEDED, reporting an
// orderly shutdown over the top of whatever startup was about to say.
//
// Being asked to stop is not a fault, so this exits 0. What must not happen is the
// teardown.
func TestAShutdownBeforeStartupTearsNothingDown(t *testing.T) {
	codes := captureExit(t)

	teardownRan := false
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error { teardownRan = true; return nil }

	// Still initializing — dialing Postgres, running migrations, connecting to the
	// broker — with phase left at phaseStarting, as it would be.
	ms := starting(t, callbacks)

	ms.ShutDownNow()

	assert.False(t, teardownRan, "teardown must not run against a service that never started")
	// Deterministic proof that the lifecycle was never entered — which is also what
	// removes the data race, since the signal goroutine no longer reads a State the
	// startup goroutine is still writing. Asserting the state is unchanged says that
	// without depending on -race tripping, which it will not do while no test drives
	// the two goroutines against each other.
	assert.Equal(t, Initializing, ms.lifecycle.State, "shutdown must not have driven the lifecycle")
	assert.NoError(t, ms.reportOutcome(ms.waitForShutdown()))
	assert.Empty(t, *codes, "being told to stop before starting is not a fault")
}

// ...which is only safe because of THIS. A startup that refused itself has already
// reported, and a stop arriving afterwards must not paint over it. That is what makes
// exiting 0 above a statement about the signal rather than about the service.
func TestARefusedStartupSurvivesTheStopThatFollowsIt(t *testing.T) {
	codes := captureExit(t)

	ms := starting(t, NewNoOpLifecycleCallbacks())

	ms.finished(errors.New("instance configuration invalid"))
	ms.ShutDownNow()

	err := ms.reportOutcome(ms.waitForShutdown())
	require.Error(t, err, "a refusal that already happened must not be overwritten by a later stop")
	assert.Contains(t, err.Error(), "instance configuration invalid")
	assert.Equal(t, []int{1}, *codes)
}

// Teardown is not idempotent, so a second shutdown must not re-run it.
func TestASecondShutdownIsIgnored(t *testing.T) {
	stops := 0
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error { stops++; return nil }
	ms := runnable(t, callbacks)

	ms.ShutDownNow()
	ms.ShutDownNow()

	assert.Equal(t, 1, stops)
	assert.NoError(t, ms.waitForShutdown())
}

// inertComponent stands in for the Microservice as the lifecycle's component so that
// Initialize and Start SUCCEED. The real one reads the instance config off a mounted
// volume, which is why nothing could exercise a successful startup before this.
type inertComponent struct{}

func (inertComponent) Initialize(context.Context) error        { return nil }
func (inertComponent) ExecuteInitialize(context.Context) error { return nil }
func (inertComponent) Start(context.Context) error             { return nil }
func (inertComponent) ExecuteStart(context.Context) error      { return nil }
func (inertComponent) Stop(context.Context) error              { return nil }
func (inertComponent) ExecuteStop(context.Context) error       { return nil }
func (inertComponent) Terminate(context.Context) error         { return nil }
func (inertComponent) ExecuteTerminate(context.Context) error  { return nil }

// 🔴 The other half of the phase gate, and the one a mutation run caught nothing
// guarding: startup must PUBLISH that it finished. If it does not, phase stays at
// phaseStarting and every shutdown reads a healthy, serving pod as one that never
// started — skipping the teardown and, worse, the readiness drain that keeps in-flight
// requests from being severed. That is every rolling update, not an edge case.
func TestAStartedServiceIsTornDownInFull(t *testing.T) {
	captureExit(t)

	stopped := make(chan struct{})
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error { close(stopped); return nil }

	ms := &Microservice{InstanceId: "valid-instance", outcome: make(chan error, 1)}
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.lifecycle = NewLifecycleManager("test", inertComponent{}, callbacks)

	ran := make(chan error, 1)
	go func() { ran <- ms.Run() }()

	require.Eventually(t, func() bool { return ms.phase.Load() == phaseRunning },
		5*time.Second, time.Millisecond, "startup never published that it had finished")
	ms.ShutDownNow()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("a fully started service was not torn down")
	}
	assert.NoError(t, <-ran)
}

// Two tests were removed from here rather than kept passing. One asserted first-wins by
// calling finished twice on one goroutine — the same property
// TestARefusedStartupSurvivesTheStopThatFollowsIt pins through the real path — while its
// comment described a concurrent race it had no concurrency for. The other guarded
// against a sender stranded forever on an undrained slot, a hazard the cap-1 buffer and
// Run's waiting receive already make impossible; it only failed at all because it sent
// three times with no receiver, a shape the process never has.

// 🔴 A COMPONENT THAT FAILS AFTER STARTUP HAD NO WAY TO SAY SO, AND THAT IS WHAT
// FailNow FIXES.
//
// A failure inside InitializeAndStart is returned, reported and exits 1. A failure a
// minute later had only ShutDownNow, which reports an ORDERLY stop — so a component
// that had irrecoverably stopped doing its job could either keep a Ready pod alive
// doing nothing, or exit 0, which is indistinguishable from a rollout to
// `kubectl get pods`, to a container-exit alert, and to anyone reading events.
//
// The exit code is the production-observable assertion; the returned error is visible
// only because captureExit's stub returns where os.Exit would not.
func TestFailNowExitsNonZeroAfterStartup(t *testing.T) {
	codes := captureExit(t)
	ms := runnable(t, NewNoOpLifecycleCallbacks())

	go ms.FailNow(errors.New("the leadership supervisor stopped"))
	err := ms.reportOutcome(ms.waitForShutdown())

	require.Error(t, err)
	require.Contains(t, err.Error(), "the leadership supervisor stopped",
		"FailNow reported some other outcome than the caller's; the reason a pod died is the whole value of it")
	require.Equal(t, []int{1}, *codes,
		"FailNow exited zero, which reads as an orderly stop — the exact ambiguity it exists to remove")
}

// 🔴 A CLEAN TEARDOWN DOES NOT MAKE A FailNow ORDERLY. The reason the process is
// going away is the caller's error, not how well it packed up — and the terminal
// send at the end of the teardown is the easiest place to lose it, because on the
// ShutDownNow path that same send is what reports success.
//
// Without this, moving FailNow's error into a variable that the teardown's final
// finished() ignores would leave every earlier assertion green and every real
// leadership failure exiting 0.
func TestFailNowKeepsItsErrorThroughAFullyCleanTeardown(t *testing.T) {
	captureExit(t)
	// A fully clean teardown: every callback succeeds, so the terminal outcome send is
	// the only thing that could carry the failure — and on the ShutDownNow path that
	// same send reports success.
	ms := runnable(t, NewNoOpLifecycleCallbacks())

	go ms.FailNow(errors.New("irrecoverable"))
	err := ms.waitForShutdown()

	require.Error(t, err, "a clean Stop/Terminate swallowed the failure that caused the shutdown")
	require.Contains(t, err.Error(), "irrecoverable")
}

// The counterweight to both: ShutDownNow still exits 0. They share a teardown, so a
// change that made failure reporting unconditional would paint every rolling update
// red — and TestAnOrderlyShutdownExitsZero above is the other half of this pair.
func TestFailNowDoesNotChangeAnOrderlyShutdown(t *testing.T) {
	codes := captureExit(t)
	ms := runnable(t, NewNoOpLifecycleCallbacks())

	go ms.ShutDownNow()
	err := ms.reportOutcome(ms.waitForShutdown())

	require.NoError(t, err)
	require.Empty(t, *codes, "an orderly shutdown set a non-zero exit status")
}
