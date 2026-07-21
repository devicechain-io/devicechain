// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
)

// withFloor sets the package Configuration to a manual floor for the duration of a
// test, restoring it after. shedAdjusted reads the floor through contentionLevel().
func withFloor(t *testing.T, level int) {
	t.Helper()
	prev := Configuration
	Configuration = &config.EventSourcesConfiguration{Contention: config.Contention{ManualFloor: level}}
	t.Cleanup(func() { Configuration = prev })
}

const (
	goldPriority       = 90
	bronzePriority     = 30
	bestEffortPriority = 10
)

// resolved wraps a priority as a resolved (cache-hit) shed-priority func.
func resolved(p int) func(string) (int, bool) { return func(string) (int, bool) { return p, true } }

// TestShedAdjustedGoldNeverShed is the promise, end to end through the wrap: a gold
// tenant's ceiling is returned untouched at EVERY level. A mutation that sheds gold
// fails here (and in the core ShedFactor test).
func TestShedAdjustedGoldNeverShed(t *testing.T) {
	base := func(string) (float64, int) { return 1000, 2000 }
	prio := resolved(goldPriority)
	for level := 0; level <= 3; level++ {
		withFloor(t, level)
		rps, burst := shedAdjusted(base, prio)("acme")
		if rps != 1000 || burst != 2000 {
			t.Errorf("gold at floor %d = (%v, %d), want the ceiling untouched (1000, 2000)", level, rps, burst)
		}
	}
}

// TestShedAdjustedDoesNotShedUnresolved pins the ADR-063 M1 fix: a tenant whose
// priority has NOT resolved (cache miss after a floor-activation restart, or UM down)
// is admitted at its base ceiling even at the deepest floor — never shed on the
// fail-safe bronze default, which could shed a gold tenant during the cold window.
func TestShedAdjustedDoesNotShedUnresolved(t *testing.T) {
	base := func(string) (float64, int) { return 1000, 2000 }
	unresolved := func(string) (int, bool) { return bestEffortPriority, false } // false = not resolved
	for level := 1; level <= 3; level++ {
		withFloor(t, level)
		rps, burst := shedAdjusted(base, unresolved)("acme")
		if rps != 1000 || burst != 2000 {
			t.Errorf("unresolved tenant at floor %d = (%v, %d), want base untouched — an unclassified tenant must not be shed", level, rps, burst)
		}
	}
}

// TestShedAdjustedLevel0IsAZeroCostFastPath pins that at floor 0 the base ceiling is
// returned untouched AND the shed priority is never resolved — the mechanism costs
// nothing until an operator sets a floor.
func TestShedAdjustedLevel0IsAZeroCostFastPath(t *testing.T) {
	withFloor(t, 0)
	base := func(string) (float64, int) { return 1000, 2000 }
	called := false
	prio := func(string) (int, bool) { called = true; return bronzePriority, true }

	rps, burst := shedAdjusted(base, prio)("acme")
	if rps != 1000 || burst != 2000 {
		t.Errorf("floor 0 = (%v, %d), want base untouched", rps, burst)
	}
	if called {
		t.Error("floor 0 resolved the shed priority — the fast path must not")
	}
}

// TestShedAdjustedShedsLowerTiers pins the ladder through the wrap: best-effort sheds
// from L1, bronze only from L2, and the reduced ceiling is a real throttle (rps below
// the base). This is the behavior the L3 gate proves live.
func TestShedAdjustedShedsLowerTiers(t *testing.T) {
	base := func(string) (float64, int) { return 1000, 2000 }

	// best-effort is shed at L1 already.
	withFloor(t, 1)
	if rps, _ := shedAdjusted(base, resolved(bestEffortPriority))("x"); rps >= 1000 {
		t.Errorf("best-effort at floor 1 rps = %v, want throttled below 1000", rps)
	}
	// bronze is NOT shed at L1 (it rides until L2).
	if rps, _ := shedAdjusted(base, resolved(bronzePriority))("x"); rps != 1000 {
		t.Errorf("bronze at floor 1 rps = %v, want the ceiling untouched (bronze sheds from L2)", rps)
	}
	// bronze IS shed at L2.
	withFloor(t, 2)
	if rps, _ := shedAdjusted(base, resolved(bronzePriority))("x"); rps >= 1000 {
		t.Errorf("bronze at floor 2 rps = %v, want throttled below 1000", rps)
	}
}
