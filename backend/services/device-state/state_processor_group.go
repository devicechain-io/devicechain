// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-device-state/processor"
)

// stateProcessorGroup is the per-shard-group state processors — one per resolved-events
// group this service reads — driven as ONE component.
//
// It is a named type with its own lifecycle verbs, rather than a slice the callbacks
// range over, so that the start and stop paths both name the same package-level
// variable. The start/stop symmetry guard (backend/tools/phaseguard) identifies a
// component by the package-level variable its verb is called on; a range loop's element
// is a local that is a different object in each callback, so a loop could never be
// matched against its own stop and was reported as unidentifiable. Through this type,
// dropping the Stop of the group is a finding naming StateProcessors.
//
// Each verb visits the processors in group order and returns the first error, exactly
// as the loops it replaced did.
type stateProcessorGroup []*processor.StateProcessor

// Start starts every processor in the group.
func (g stateProcessorGroup) Start(ctx context.Context) error {
	for _, sp := range g {
		if err := sp.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops every processor in the group.
func (g stateProcessorGroup) Stop(ctx context.Context) error {
	for _, sp := range g {
		if err := sp.Stop(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Terminate terminates every processor in the group.
func (g stateProcessorGroup) Terminate(ctx context.Context) error {
	for _, sp := range g {
		if err := sp.Terminate(ctx); err != nil {
			return err
		}
	}
	return nil
}
