// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/rs/zerolog/log"
)

type LifecycleState int64

// Enumeration of lifecycle states
//
//go:generate stringer -type=LifecycleState
const (
	Uninitialized LifecycleState = iota
	Initializing
	Initialized
	Starting
	Started
	Stopping
	Stopped
	Terminating
	Terminated
)

// Common lifecycle concept for components
type LifecycleComponent interface {
	// Invokes lifecycle initialization.
	Initialize(ctx context.Context) error

	// Initialize component. Happens once on startup.
	ExecuteInitialize(context.Context) error

	// Invokes lifecycle startup.
	Start(context.Context) error

	// Start component. Runs once per successful startup: a stop is terminal, so there
	// is no start after one. A FAILED start is the exception — it restores the
	// component to Initialized, so a caller that retries re-enters this.
	ExecuteStart(context.Context) error

	// Invokes lifecycle shutdown.
	Stop(context.Context) error

	// Stop a started component.
	ExecuteStop(context.Context) error

	// Invokes lifecycle termination.
	Terminate(context.Context) error

	// Terminate component.
	ExecuteTerminate(context.Context) error
}

// Callback used to add behavior to a lifecycle component.
type LifecycleCallback struct {
	// Processing that occurs before component lifecycle step.
	Preprocess func(context.Context) error

	// Processing that occurs before component lifecycle step.
	Postprocess func(context.Context) error
}

// Provides a lifecycle callback with no-op implementations
func NewNoOpLifecycleCallback() LifecycleCallback {
	return LifecycleCallback{
		Preprocess: func(ctx context.Context) error {
			return nil
		},
		Postprocess: func(ctx context.Context) error {
			return nil
		},
	}
}

// Lifecycle callbacks that may be triggered by lifecycle manager.
type LifecycleCallbacks struct {
	Initializer LifecycleCallback
	Starter     LifecycleCallback
	Stopper     LifecycleCallback
	Terminator  LifecycleCallback
}

// Provides lifecycle callbacks with all no-op implementations.
func NewNoOpLifecycleCallbacks() LifecycleCallbacks {
	return LifecycleCallbacks{
		Initializer: NewNoOpLifecycleCallback(),
		Starter:     NewNoOpLifecycleCallback(),
		Stopper:     NewNoOpLifecycleCallback(),
		Terminator:  NewNoOpLifecycleCallback(),
	}
}

type LifecycleManager struct {
	Name      string
	Component LifecycleComponent
	Callbacks LifecycleCallbacks
	State     LifecycleState
}

// Create a new lifecycle manager
func NewLifecycleManager(name string, component LifecycleComponent, callbacks LifecycleCallbacks) LifecycleManager {
	mgr := LifecycleManager{Name: name, Component: component, Callbacks: callbacks, State: Uninitialized}
	return mgr
}

// Set lifecycle state on manager and print the updated state
func (mgr *LifecycleManager) SetLifecycleState(state LifecycleState) {
	log.Info().Str("component", mgr.Name).Str("state", state.String()).Msg("Updating lifecycle state")
	mgr.State = state
}

// The states each lifecycle step may be entered FROM.
//
// 🔴 THESE ARE ALLOW LISTS, AND THE POLARITY IS THE ENTIRE POINT. Every one of these
// guards used to be written the other way round — six `if State == X { return error }`
// clauses on Start, six on Stop — which makes the default PERMIT: any state nobody
// thought to name is legal by omission. Nobody ever decided that a stop from Initializing
// was allowed. It simply was not on the list, and a permission granted by omission reads
// exactly like one that was chosen.
//
// That default is what a SIGTERM landing during initialization fell through: teardown ran
// in full against a service that did not exist yet, SUCCEEDED, reported an orderly
// shutdown, claimed the process's one outcome slot, and exited 0 for a service that never
// started. The fix for that lives in Microservice.ShutDownNow, which is the right place
// for it — but the guards should not have been the thing that let it through.
//
// LifecycleState is a stringer-generated enum, and enums gain members. Written this way a
// new state is refused by every step until someone names it; written the other way it is
// permitted by every step until someone remembers it.
var (
	initializeFrom = []LifecycleState{Uninitialized}

	// 🔴 Stopped IS DELIBERATELY ABSENT FROM startFrom. A stop is TERMINAL for a
	// component: this state machine offers no route back to a started one, and a
	// start attempted from Stopped is refused at the point it is asked for.
	//
	// It used to be permitted, on the grounds that a start after a stop was a
	// supported sequence. It was not one, and nothing in the tree ever performed it.
	// Initialize is legal only from Uninitialized, so nothing re-establishes what a
	// stop tore down — most concretely the broker connection, which
	// NatsManager.ExecuteStop drains and which no start path re-dials. A second start
	// therefore entered the component, rebuilt readers against a closed connection,
	// and failed only after spending a full infrastructure retry budget, reporting
	// the BROKER as unreachable. That named the wrong thing twice over: the sequence
	// was impossible at the moment it was requested, not thirty attempts later, and
	// the fault was in the caller rather than in the infrastructure it accused.
	//
	// The remedy for a stopped component is a new process. That is what the
	// deployment already provides, and what ReadPacer's give-up path already
	// documents as the remedy for the broker faults it reports: a replacement pod
	// re-dials, re-creates its durable consumers, and re-reads its mounted
	// credentials, none of which an in-process second start does.
	//
	// ⚠️ THIS DOES NOT MAKE ExecuteStart A ONCE-PER-PROCESS HOOK, and anything that
	// registers with a process-scoped registry must not assume that it is.
	// transition restores the previous state when any step fails, so a start that
	// fails — inside ExecuteStart, or in a Starter callback around it — leaves the
	// component Initialized and startable again, with whatever the failed attempt
	// already built still built. NewNatsManager spells out what that costs a callback
	// that constructs Prometheus collectors.
	startFrom = []LifecycleState{Initialized}

	// ⚠️ Initialized is on stopFrom DELIBERATELY, and narrowing it to Started alone is a
	// change that looks like a tightening and is a defect. Two things argue against it,
	// and both were found by going looking rather than by reasoning about the states:
	//
	//   - A REFUSAL CASCADES. Every service's beforeMicroserviceStopped stops its
	//     components in a loop that returns on the first error, so one refused stop
	//     leaves every component after it un-stopped AND un-terminated. Not
	//     hypothetical — it is the rdb.Guest defect (see rdb/guest.go): a component that
	//     was initialized but never started had its Terminate refused by this state
	//     machine, and its caller returned early, taking the broker and the service's
	//     own database down un-terminated with it.
	//   - "Initialized" DOES NOT MEAN "nothing has run". Start restores the previous
	//     state when ExecuteStart fails, so a component whose start failed HALFWAY reads
	//     as Initialized with work already in flight — MqttEventSource spawns its decode
	//     worker goroutines before the subscribe that can fail. Skipping its teardown
	//     would strand them.
	//
	// What IS refused is Initializing: a component mid-initialization on another
	// goroutine, about which no caller can reason at all. Nothing in the tree reaches
	// that today — ShutDownNow's phase gate claims the process before any Stop is
	// issued, and sub-component stops run only from a microservice that finished
	// starting — so this is the second line, not the first.
	stopFrom = []LifecycleState{Initialized, Started}

	terminateFrom = []LifecycleState{Stopped}
)

// transition runs one lifecycle step: guard the starting state, announce the transient
// one, run preprocess → execute → postprocess, and land on the final state. Any failure
// puts the state back where it was found.
//
// The four steps below were four copies of this, and each copy wrote the restore-and-
// return block three times. Twelve chances for one to be missing, where the miss would
// surface only as a component stuck in Starting long after the failure that put it there
// was reported and handled.
func (mgr *LifecycleManager) transition(ctx context.Context, verb string, from []LifecycleState,
	during LifecycleState, cb LifecycleCallback, exec func(context.Context) error, done LifecycleState) error {
	if !slices.Contains(from, mgr.State) {
		return fmt.Errorf("cannot %s component %q while it is %s (permitted: %s)",
			verb, mgr.Name, mgr.State, permittedStates(from))
	}
	prev := mgr.State
	mgr.SetLifecycleState(during)
	for _, step := range []func(context.Context) error{cb.Preprocess, exec, cb.Postprocess} {
		if err := step(ctx); err != nil {
			mgr.SetLifecycleState(prev)
			return err
		}
	}
	mgr.SetLifecycleState(done)
	return nil
}

// permittedStates renders an allow list into the refusal a caller actually reads. The
// state it was IN is only half the answer; without the half it was allowed to be in, the
// message says a call was wrong without saying what would have been right.
func permittedStates(from []LifecycleState) string {
	names := make([]string, 0, len(from))
	for _, state := range from {
		names = append(names, state.String())
	}
	return strings.Join(names, " or ")
}

// Handle component initialization
func (mgr *LifecycleManager) Initialize(ctx context.Context) error {
	return mgr.transition(ctx, "initialize", initializeFrom, Initializing,
		mgr.Callbacks.Initializer, mgr.Component.ExecuteInitialize, Initialized)
}

// Handle component startup
func (mgr *LifecycleManager) Start(ctx context.Context) error {
	return mgr.transition(ctx, "start", startFrom, Starting,
		mgr.Callbacks.Starter, mgr.Component.ExecuteStart, Started)
}

// Handle component shutdown
func (mgr *LifecycleManager) Stop(ctx context.Context) error {
	return mgr.transition(ctx, "stop", stopFrom, Stopping,
		mgr.Callbacks.Stopper, mgr.Component.ExecuteStop, Stopped)
}

// Handle component termination
func (mgr *LifecycleManager) Terminate(ctx context.Context) error {
	return mgr.transition(ctx, "terminate", terminateFrom, Terminating,
		mgr.Callbacks.Terminator, mgr.Component.ExecuteTerminate, Terminated)
}
