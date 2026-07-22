// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package loadtest is the ADR-064 correctness-under-load harness: a driver +
// oracle layered on the sim subsystem (ADR-035/050), not a second sim and not a
// backdoor. The driver reuses the sim's seeded-deterministic populations, real
// device-plane ingress, and per-tenant governance; the oracle reads back
// resolved/persisted platform truth over the same tenant-scoped GraphQL a real
// client uses (the fidelity rule) and asserts feature invariants still hold at
// load.
//
// This is L1 — the aggregate-reconciliation base layer of the execution spec
// (research/load-test-harness.md §4.1): drive a known, counted volume of events
// and assert `persisted == accepted` after quiesce, the whole-fleet completeness
// check that catches the at-most-once dropped-event class (ADR-030) at O(devices)
// cost. Planted probes (alarm/command/detection/isolation), the safety-continuous
// live monitor, and the tiered ADR-063 contention profile layer on top of it in
// later slices.
package loadtest

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Default quiesce parameters. After the driver stops emitting, persisted rows
// keep landing as the pipeline drains its lag; the oracle polls the windowed
// event count until it reaches the accepted target, then reconciles.
// QuiesceTimeout backstops a count that never reaches the target — a drop, or
// lag that never drained; itself a finding, not a pass (research/load-test-harness.md §4).
const (
	DefaultQuiescePoll    = 2 * time.Second
	DefaultQuiesceTimeout = 2 * time.Minute
	// DefaultQuiesceSettle is how long the oracle keeps watching the count after it
	// first reaches the accepted target, to catch a PROMPT over-persist (e.g. a
	// resolve/detect/dispatch path that double-writes within a few seconds) that
	// exit-on-first-reach would otherwise miss as a false completeness pass. It is
	// deliberately NOT sized to the redelivery window: a redelivery double-persist
	// lands up to ackWait (60s, core/messaging) after the lost ack, and that
	// induced-redelivery exactly-once class is owned by the durability rig
	// (ADR-030, core/messaging lifecycle_durability_test) which deliberately forces
	// the restart/gap — paying a flat 60s settle on every run to re-prove it here
	// would only duplicate that coverage. 5s matches the alarm/command settle and
	// keeps local iteration cheap; a longer watch is available via --quiesce-settle.
	// (BOUNDED OBSERVATION: an over-persist beyond this window is out of scope.)
	DefaultQuiesceSettle = 5 * time.Second
	// DefaultHold is the steady-state emit window for a modest CI-tier run.
	DefaultHold = 30 * time.Second
	// DefaultMinAccepted is the floor of accepted events a run must apply before
	// its verdict counts — a release GATE must exercise real load, so a job that
	// lost its load flags and drove a handful of events fails rather than
	// certifying "correctness under load" it never tested.
	DefaultMinAccepted = 1000
)

// Profile is one load-test run's configuration. Manifest/Seed/Devices/
// EmitInterval/Concurrency size the driver (reusing sim.Load's knobs — the #457
// configurable-load work); Hold is how long to emit at that load; the Quiesce*
// fields tune the oracle's settle detection.
//
// Determinism is a hard ADR-064 requirement: (Manifest, Seed, Devices,
// EmitInterval) fully determine which tokens emit what, so a correctness failure
// is reproducible. Hold and the Quiesce* fields affect how much load is applied
// and how long the oracle waits, not what is emitted.
type Profile struct {
	// Manifest is the sim scenario id (e.g. "buildingpulse"); Seed makes its
	// population deterministic (ADR-050).
	Manifest string
	Seed     int64

	// Devices overrides the scenario's population size (0 = the scenario's own
	// count). EmitInterval overrides the tick cadence (0 = the 5s demo cadence).
	// Concurrency bounds in-flight emits (0 = derived from device count).
	Devices      int
	EmitInterval time.Duration
	Concurrency  int

	// Hold is how long to emit at steady state before stopping and reconciling.
	Hold time.Duration

	// MinAccepted is the floor of accepted events the run must apply for its
	// verdict to count (0 = DefaultMinAccepted). Below it the run fails as a
	// trivial smoke rather than certifying load it never applied.
	MinAccepted int64

	// QuiescePoll/QuiesceTimeout tune the oracle's read-back cadence and the
	// backstop for a count that never reaches the accepted target; QuiesceSettle is
	// how long the oracle keeps watching after first-reach to catch a late
	// over-persist (see the Default* constants).
	QuiescePoll    time.Duration
	QuiesceTimeout time.Duration
	QuiesceSettle  time.Duration
}

// Load returns the sim load profile this run drives with.
func (p Profile) Load() sim.Load {
	return sim.Load{
		DeviceCount:  p.Devices,
		EmitInterval: p.EmitInterval,
		Concurrency:  p.Concurrency,
	}
}

// withDefaults fills unset optional fields with their defaults. It does not
// touch the deterministic emission inputs (Manifest/Seed/Devices/EmitInterval).
func (p Profile) withDefaults() Profile {
	if p.Hold <= 0 {
		p.Hold = DefaultHold
	}
	if p.MinAccepted <= 0 {
		p.MinAccepted = DefaultMinAccepted
	}
	if p.QuiescePoll <= 0 {
		p.QuiescePoll = DefaultQuiescePoll
	}
	if p.QuiesceTimeout <= 0 {
		p.QuiesceTimeout = DefaultQuiesceTimeout
	}
	if p.QuiesceSettle <= 0 {
		p.QuiesceSettle = DefaultQuiesceSettle
	}
	return p
}

// Validate rejects a profile that cannot be run as asked rather than silently
// substituting a value — the same house rule as sim.Load.Validate. It also
// enforces that the sim load profile is itself legal.
func (p Profile) Validate() error {
	if p.Manifest == "" {
		return fmt.Errorf("manifest is required")
	}
	if p.Hold < 0 {
		return fmt.Errorf("hold %s is negative", p.Hold)
	}
	if p.QuiescePoll < 0 || p.QuiesceTimeout < 0 || p.QuiesceSettle < 0 {
		return fmt.Errorf("quiesce poll/timeout/settle must not be negative")
	}
	if p.MinAccepted < 0 {
		return fmt.Errorf("min accepted %d is negative", p.MinAccepted)
	}
	return p.Load().Validate()
}
