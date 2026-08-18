// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// recordingComponent is an inert LifecycleComponent that records which hooks ran, can be
// told to fail one of them, and can run arbitrary code from inside a hook.
//
// That last part is not a convenience. Initializing exists only while ExecuteInitialize is
// on the stack, so a hook is the only place from which a manager can be observed in it —
// which is exactly the situation being guarded against, a stop arriving while startup is
// still running on another goroutine.
type recordingComponent struct {
	calls    []string
	failOn   string
	inHook   map[string]func()
	lifetime *LifecycleManager
}

func (c *recordingComponent) record(name string) error {
	if fn, ok := c.inHook[name]; ok {
		fn()
	}
	c.calls = append(c.calls, name)
	if c.failOn == name {
		return fmt.Errorf("%s failed", name)
	}
	return nil
}

func (c *recordingComponent) Initialize(ctx context.Context) error {
	return c.lifetime.Initialize(ctx)
}
func (c *recordingComponent) Start(ctx context.Context) error { return c.lifetime.Start(ctx) }
func (c *recordingComponent) Stop(ctx context.Context) error  { return c.lifetime.Stop(ctx) }
func (c *recordingComponent) Terminate(ctx context.Context) error {
	return c.lifetime.Terminate(ctx)
}

func (c *recordingComponent) ExecuteInitialize(context.Context) error {
	return c.record("ExecuteInitialize")
}
func (c *recordingComponent) ExecuteStart(context.Context) error { return c.record("ExecuteStart") }
func (c *recordingComponent) ExecuteStop(context.Context) error  { return c.record("ExecuteStop") }
func (c *recordingComponent) ExecuteTerminate(context.Context) error {
	return c.record("ExecuteTerminate")
}

// newRecorded wires a component to a manager whose callbacks record too, so a test sees
// the whole sequence — callbacks included — and not just the Execute hooks.
func newRecorded() (*recordingComponent, *LifecycleManager) {
	component := &recordingComponent{inHook: map[string]func(){}}
	step := func(prefix string) LifecycleCallback {
		return LifecycleCallback{
			Preprocess:  func(context.Context) error { return component.record(prefix + ".pre") },
			Postprocess: func(context.Context) error { return component.record(prefix + ".post") },
		}
	}
	mgr := NewLifecycleManager("recorded", component, LifecycleCallbacks{
		Initializer: step("initializer"),
		Starter:     step("starter"),
		Stopper:     step("stopper"),
		Terminator:  step("terminator"),
	})
	component.lifetime = &mgr
	return component, &mgr
}

func TestFullLifecycleRunsEveryHookInOrder(t *testing.T) {
	component, mgr := newRecorded()
	ctx := context.Background()
	for _, call := range []func(context.Context) error{mgr.Initialize, mgr.Start, mgr.Stop, mgr.Terminate} {
		if err := call(ctx); err != nil {
			t.Fatalf("lifecycle step failed: %v", err)
		}
	}
	want := []string{
		"initializer.pre", "ExecuteInitialize", "initializer.post",
		"starter.pre", "ExecuteStart", "starter.post",
		"stopper.pre", "ExecuteStop", "stopper.post",
		"terminator.pre", "ExecuteTerminate", "terminator.post",
	}
	if !slices.Equal(component.calls, want) {
		t.Errorf("hook order:\n got %v\nwant %v", component.calls, want)
	}
	if mgr.State != Terminated {
		t.Errorf("final state = %s, want Terminated", mgr.State)
	}
}

// A start after a stop is a supported sequence, not an accident of the guard being loose.
// GatewayJetStreamSource builds its channels per-Start and says so in its own comment
// because of this; narrowing startFrom to Initialized alone would break it silently, since
// nothing else in the tree exercises the second start.
func TestStartIsPermittedAfterStop(t *testing.T) {
	_, mgr := newRecorded()
	ctx := context.Background()
	for _, call := range []func(context.Context) error{mgr.Initialize, mgr.Start, mgr.Stop, mgr.Start} {
		if err := call(ctx); err != nil {
			t.Fatalf("lifecycle step failed: %v", err)
		}
	}
	if mgr.State != Started {
		t.Errorf("state after restart = %s, want Started", mgr.State)
	}
}

// The behavioural change: a stop arriving while initialization is still running is now
// refused. Nothing in the tree reaches it — ShutDownNow's phase gate claims the process
// first — so this is the second line, and it is the state about which no caller can
// reason at all, since the component is halfway through building itself.
func TestStopIsRefusedWhileInitializationIsStillRunning(t *testing.T) {
	component, mgr := newRecorded()
	var stopErr error
	var seen LifecycleState
	component.inHook["ExecuteInitialize"] = func() {
		seen = mgr.State
		stopErr = mgr.Stop(context.Background())
	}
	if err := mgr.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// Without this the test proves nothing: a stop refused because the component was in
	// some OTHER state would read identically.
	if seen != Initializing {
		t.Fatalf("hook observed state %s, want Initializing — the test never reproduced the case", seen)
	}
	if stopErr == nil {
		t.Fatal("a stop during initialization was permitted")
	}
	if slices.Contains(component.calls, "ExecuteStop") || slices.Contains(component.calls, "stopper.pre") {
		t.Errorf("a refused stop ran teardown anyway: %v", component.calls)
	}
	if mgr.State != Initialized {
		t.Errorf("state after the refused stop = %s, want Initialized", mgr.State)
	}
}

func TestStartIsRefusedWhileInitializationIsStillRunning(t *testing.T) {
	component, mgr := newRecorded()
	var startErr error
	component.inHook["ExecuteInitialize"] = func() {
		startErr = mgr.Start(context.Background())
	}
	if err := mgr.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if startErr == nil {
		t.Fatal("a start during initialization was permitted")
	}
	if slices.Contains(component.calls, "ExecuteStart") {
		t.Errorf("a refused start ran startup anyway: %v", component.calls)
	}
}

// 🔴 THE COUNTERWEIGHT, and the one to read before "tightening" stopFrom to Started alone.
// Two things make that a defect rather than a fix:
//
//   - a refusal cascades. Every beforeMicroserviceStopped stops components in a loop that
//     returns on the first error, so one refused stop leaves every later component
//     un-stopped and un-terminated — the rdb.Guest defect, which is why Guest has no
//     lifecycle manager at all today.
//   - Initialized does not mean nothing ran. Start restores the previous state when
//     ExecuteStart fails, so a component whose start failed halfway reads as Initialized
//     with goroutines already spawned.
func TestStopIsPermittedFromInitialized(t *testing.T) {
	component, mgr := newRecorded()
	ctx := context.Background()
	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := mgr.Stop(ctx); err != nil {
		t.Fatalf("a stop from Initialized was refused: %v", err)
	}
	if !slices.Contains(component.calls, "ExecuteStop") {
		t.Errorf("teardown did not run: %v", component.calls)
	}
	// Terminate refuses every state but Stopped, so this is what a component that was
	// initialized and never started actually releases its resources through.
	if err := mgr.Terminate(ctx); err != nil {
		t.Fatalf("Terminate after a stop from Initialized was refused: %v", err)
	}
}

// A failure anywhere in the three-step body puts the state back where it was found. The
// restore used to be written out once per step per lifecycle method — twelve copies — and
// this is what a missing one would look like: a component parked in Starting forever after
// a failure that was reported and handled.
func TestAFailedStepPutsTheStateBack(t *testing.T) {
	for _, failOn := range []string{"starter.pre", "ExecuteStart", "starter.post"} {
		t.Run(failOn, func(t *testing.T) {
			component, mgr := newRecorded()
			ctx := context.Background()
			if err := mgr.Initialize(ctx); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			component.failOn = failOn
			if err := mgr.Start(ctx); err == nil {
				t.Fatal("Start reported success despite a failing step")
			}
			if mgr.State != Initialized {
				t.Errorf("state after a failed start = %s, want Initialized", mgr.State)
			}
			// The steps after the failure must not have run.
			if idx := slices.Index(component.calls, failOn); idx != len(component.calls)-1 {
				t.Errorf("steps ran after the failure: %v", component.calls)
			}
		})
	}
}

func TestARefusalNamesTheStateAndWhatWasPermitted(t *testing.T) {
	_, mgr := newRecorded()
	mgr.State = Terminated
	err := mgr.Stop(context.Background())
	if err == nil {
		t.Fatal("a stop from Terminated was permitted")
	}
	// Naming only the state a call was IN says it was wrong without saying what would
	// have been right, which is the half an operator reading a shutdown log needs.
	for _, want := range []string{"Terminated", "Initialized", "Started"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %s", err, want)
		}
	}
}

// namedStates enumerates the LifecycleState enum by asking the stringer where it ends: an
// unnamed value renders as "LifecycleState(N)". A state added tomorrow is therefore
// covered by the test below tomorrow, rather than whenever somebody remembers to extend a
// literal list here — which is the same failure mode, in a test, that deny-list guards
// had in the code.
func namedStates() []LifecycleState {
	states := make([]LifecycleState, 0)
	for i := 0; ; i++ {
		state := LifecycleState(i)
		if strings.HasPrefix(state.String(), "LifecycleState(") {
			return states
		}
		states = append(states, state)
	}
}

// The polarity itself: every state outside a step's allow list is refused, and refused
// before any hook runs. This is what a deny list could not give — there, a state nobody
// named was permitted, and the guard could not tell "allowed" from "forgotten".
//
// 🔴 THE PERMITTED SETS ARE WRITTEN OUT HERE AS LITERALS, NOT READ FROM initializeFrom
// AND FRIENDS. Taking them from the production variables makes this test agree with
// whatever those variables say — widen one and the expectation widens with it, in
// silence. A mutation harness proved that precisely: adding Initialized to initializeFrom
// (re-initialization) and to terminateFrom (terminate without stopping) both SURVIVED
// against the deriving version of this test, while every other list mutation died. The
// list is the specification, so the specification has to be stated somewhere it cannot
// be edited by the code it governs.
func TestEveryStateOutsideTheAllowListIsRefused(t *testing.T) {
	states := namedStates()
	// A vacuous pass here would be indistinguishable from a thorough one, and the whole
	// point of deriving the enum is that the count is not written down anywhere else.
	if len(states) < 9 {
		t.Fatalf("enumerated only %d states; the stringer probe is broken, not the guards", len(states))
	}

	steps := []struct {
		verb      string
		permitted []LifecycleState
		call      func(*LifecycleManager, context.Context) error
	}{
		{"initialize", []LifecycleState{Uninitialized}, (*LifecycleManager).Initialize},
		{"start", []LifecycleState{Initialized, Stopped}, (*LifecycleManager).Start},
		{"stop", []LifecycleState{Initialized, Started}, (*LifecycleManager).Stop},
		{"terminate", []LifecycleState{Stopped}, (*LifecycleManager).Terminate},
	}
	// Both halves are asserted. A test that only checked the refusals would stay green
	// against a guard that refuses everything, which is the mirror image of the defect
	// being fixed and just as silent.
	for _, step := range steps {
		for _, state := range states {
			component, mgr := newRecorded()
			mgr.State = state
			err := step.call(mgr, context.Background())

			if slices.Contains(step.permitted, state) {
				if err != nil {
					t.Errorf("%s from %s was refused: %v", step.verb, state, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s from %s was permitted", step.verb, state)
			}
			if len(component.calls) != 0 {
				t.Errorf("%s from %s ran %v before refusing", step.verb, state, component.calls)
			}
			if mgr.State != state {
				t.Errorf("%s from %s left the state at %s", step.verb, state, mgr.State)
			}
		}
	}
}
