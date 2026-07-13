// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package trace

import (
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
)

// find returns the step for a node id (and whether it was present).
func find(steps []Step, nodeID string) (Step, bool) {
	for _, s := range steps {
		if s.NodeID == nodeID {
			return s, true
		}
	}
	return Step{}, false
}

// mustDisp fails unless node's disposition matches want.
func mustDisp(t *testing.T, steps []Step, nodeID, want string) {
	t.Helper()
	s, ok := find(steps, nodeID)
	if !ok {
		t.Fatalf("no trace step for node %q", nodeID)
	}
	if s.Disposition != want {
		t.Fatalf("node %q disposition = %q, want %q (detail: %q)", nodeID, s.Disposition, want, s.Detail)
	}
}

// plan is a source→condition→b→act(raiseAlarm) canvas plan with one branch guard `value > 100.0`.
func planWithGuard(when string) graph.NodeTracePlan {
	return graph.NodeTracePlan{
		SourceID:    "s",
		ConditionID: "c",
		Actions: []graph.ActionPath{{
			NodeID:   "a",
			Type:     string(rules.ActionRaiseAlarm),
			Branches: []graph.BranchStep{{NodeID: "b", When: when}},
		}},
	}
}

// TestRaiseGuardPasses: on a rising edge whose value satisfies the branch guard, the branch passes
// and the raiseAlarm action raises; the source delivered and the condition raised.
func TestRaiseGuardPasses(t *testing.T) {
	b := NewBuilder(planWithGuard("value > 100.0"))
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 150, HasValue: true})
	mustDisp(t, steps, "s", DispDelivered)
	mustDisp(t, steps, "c", DispRaised)
	mustDisp(t, steps, "b", DispPassed)
	mustDisp(t, steps, "a", DispRaised)
}

// TestRaiseGuardBlocks: on a rising edge whose value fails the branch guard, the branch blocks and
// the action is skipped.
func TestRaiseGuardBlocks(t *testing.T) {
	b := NewBuilder(planWithGuard("value > 100.0"))
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 50, HasValue: true})
	mustDisp(t, steps, "c", DispRaised)
	mustDisp(t, steps, "b", DispBlocked)
	mustDisp(t, steps, "a", DispSkipped)
}

// TestResolveClearsUnconditionally: a falling edge clears the raiseAlarm regardless of the branch
// guard (the branch is NOT evaluated — the 9c invariant). Even a value that would fail the guard on
// a raise still clears on a resolve.
func TestResolveClearsUnconditionally(t *testing.T) {
	b := NewBuilder(planWithGuard("value > 100.0"))
	steps := b.Build(Firing{Raise: false, Series: "dev1", Value: 50, HasValue: true})
	mustDisp(t, steps, "c", DispResolved)
	mustDisp(t, steps, "b", DispSkipped) // not evaluated on a resolve
	mustDisp(t, steps, "a", DispCleared)
}

// TestSendCommandInertOnResolve: a sendCommand action has no falling-edge twin, so a resolve leaves
// it inert (and its branch is not evaluated).
func TestSendCommandInertOnResolve(t *testing.T) {
	plan := graph.NodeTracePlan{
		SourceID: "s", ConditionID: "c",
		Actions: []graph.ActionPath{{NodeID: "a", Type: string(rules.ActionSendCommand), Branches: []graph.BranchStep{{NodeID: "b", When: "value > 0.0"}}}},
	}
	b := NewBuilder(plan)
	steps := b.Build(Firing{Raise: false, Series: "dev1", Value: 10, HasValue: true})
	mustDisp(t, steps, "b", DispSkipped)
	mustDisp(t, steps, "a", DispInert)
}

// TestSendCommandSentOnRaise: a sendCommand fires ("sent") on a rising edge whose guard passes.
func TestSendCommandSentOnRaise(t *testing.T) {
	plan := graph.NodeTracePlan{
		SourceID: "s", ConditionID: "c",
		Actions: []graph.ActionPath{{NodeID: "a", Type: string(rules.ActionSendCommand), Branches: nil}},
	}
	b := NewBuilder(plan)
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 10, HasValue: true})
	mustDisp(t, steps, "a", DispSent)
}

// TestChainedBranchesShortCircuit: with two branches, the first blocking one stops the walk — the
// second branch is "skipped" (unreached) and the action is skipped.
func TestChainedBranchesShortCircuit(t *testing.T) {
	plan := graph.NodeTracePlan{
		SourceID: "s", ConditionID: "c",
		Actions: []graph.ActionPath{{
			NodeID: "a", Type: string(rules.ActionRaiseAlarm),
			Branches: []graph.BranchStep{{NodeID: "b1", When: "value > 100.0"}, {NodeID: "b2", When: "value > 0.0"}},
		}},
	}
	b := NewBuilder(plan)
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 50, HasValue: true})
	mustDisp(t, steps, "b1", DispBlocked)
	mustDisp(t, steps, "b2", DispSkipped) // never reached — an earlier branch blocked
	mustDisp(t, steps, "a", DispSkipped)
}

// TestValuelessFiringGuardIsCleanFalse: a value-less firing (HasValue false) makes `value > x` a
// clean false (not an error), so a value-referencing branch blocks rather than fails.
func TestValuelessFiringGuardIsCleanFalse(t *testing.T) {
	b := NewBuilder(planWithGuard("value > 0.0"))
	steps := b.Build(Firing{Raise: true, Series: "dev1", HasValue: false})
	mustDisp(t, steps, "b", DispBlocked)
	mustDisp(t, steps, "a", DispSkipped)
}

// TestSeriesGuard: a branch keyed on the series binds the firing's series.
func TestSeriesGuard(t *testing.T) {
	b := NewBuilder(planWithGuard(`series == "dev1"`))
	pass := b.Build(Firing{Raise: true, Series: "dev1", HasValue: false})
	mustDisp(t, pass, "b", DispPassed)
	block := b.Build(Firing{Raise: true, Series: "other", HasValue: false})
	mustDisp(t, block, "b", DispBlocked)
}

// TestUncompilableGuardFailsClosed: a builder over a plan whose guard cannot compile blocks the
// branch (fail closed) rather than passing it un-gated — mirroring the REACT dispatcher.
func TestUncompilableGuardFailsClosed(t *testing.T) {
	b := NewBuilder(planWithGuard("this is not @@ valid CEL"))
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 10, HasValue: true})
	s, ok := find(steps, "b")
	if !ok || s.Disposition != DispBlocked {
		t.Fatalf("uncompilable guard should block; got %+v", s)
	}
	if s.Detail == "" {
		t.Fatal("a fail-closed block should carry a detail reason")
	}
	mustDisp(t, steps, "a", DispSkipped)
}

// TestStepOrder: steps come out source → condition → branch(es) → action.
func TestStepOrder(t *testing.T) {
	b := NewBuilder(planWithGuard("value > 0.0"))
	steps := b.Build(Firing{Raise: true, Series: "dev1", Value: 10, HasValue: true})
	order := make([]string, len(steps))
	for i, s := range steps {
		order[i] = s.NodeID
	}
	want := []string{"s", "c", "b", "a"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("step order = %v, want %v", order, want)
		}
	}
}
