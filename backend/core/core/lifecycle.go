/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package core

import (
	"context"
	"errors"

	"github.com/rs/zerolog/log"
)

type LifecycleState int64

// Enumeration of lifecycle states
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

	// Start component. May happen on startup or after stop.
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

// Handle component initialization
func (mgr *LifecycleManager) Initialize(ctx context.Context) error {
	if mgr.State != Uninitialized {
		return errors.New("attempting to initialize component that is already initialized")
	}
	prev := mgr.State
	mgr.SetLifecycleState(Initializing)

	// Run callbacks that precede initialization
	err := mgr.Callbacks.Initializer.Preprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run primary initialization functionality
	err = mgr.Component.ExecuteInitialize(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run callbacks that follow initialization
	err = mgr.Callbacks.Initializer.Postprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	mgr.SetLifecycleState(Initialized)
	return nil
}

// Handle component startup
func (mgr *LifecycleManager) Start(ctx context.Context) error {
	if mgr.State == Uninitialized {
		return errors.New("attempting to start an uninitialized component")
	}
	if mgr.State == Starting {
		return errors.New("attempting to start a component that is already starting")
	}
	if mgr.State == Started {
		return errors.New("attempting to start a component that is already started")
	}
	if mgr.State == Stopping {
		return errors.New("attempting to start a component that is stopping")
	}
	if mgr.State == Terminating {
		return errors.New("attempting to start a component that is terminating")
	}
	if mgr.State == Terminated {
		return errors.New("attempting to start a component that is terminated")
	}
	prev := mgr.State
	mgr.SetLifecycleState(Starting)

	// Run callbacks that precede startup
	err := mgr.Callbacks.Starter.Preprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run primary startup functionality
	err = mgr.Component.ExecuteStart(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run callbacks that follow startup
	err = mgr.Callbacks.Starter.Postprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	mgr.SetLifecycleState(Started)
	return nil
}

// Handle component shutdown
func (mgr *LifecycleManager) Stop(ctx context.Context) error {
	if mgr.State == Uninitialized {
		return errors.New("attempting to stop an uninitialized component")
	}
	if mgr.State == Starting {
		return errors.New("attempting to stop a component that is partially started")
	}
	if mgr.State == Stopping {
		return errors.New("attempting to stop a component that is already stopping")
	}
	if mgr.State == Stopped {
		return errors.New("attempting to stop a component that is already stopped")
	}
	if mgr.State == Terminating {
		return errors.New("attempting to stop a component that is terminating")
	}
	if mgr.State == Terminated {
		return errors.New("attempting to stop a component that is terminated")
	}
	prev := mgr.State
	mgr.SetLifecycleState(Stopping)

	// Run callbacks that precede shutdown
	err := mgr.Callbacks.Stopper.Preprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run primary shutdown functionality
	err = mgr.Component.ExecuteStop(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run callbacks that follow shutdown
	err = mgr.Callbacks.Stopper.Postprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	mgr.SetLifecycleState(Stopped)
	return nil
}

// Handle component termination
func (mgr *LifecycleManager) Terminate(ctx context.Context) error {
	if mgr.State == Uninitialized {
		return errors.New("attempting to terminate component that is not initialized")
	}
	if mgr.State != Stopped {
		return errors.New("attempting to terminate component that is not stopped")
	}
	prev := mgr.State
	mgr.SetLifecycleState(Terminating)

	// Run callbacks that precede terminate
	err := mgr.Callbacks.Terminator.Preprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run primary terminate functionality
	err = mgr.Component.ExecuteTerminate(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	// Run callbacks that follow terminate
	err = mgr.Callbacks.Terminator.Postprocess(ctx)
	if err != nil {
		mgr.SetLifecycleState(prev)
		return err
	}

	mgr.SetLifecycleState(Terminated)
	return nil
}
