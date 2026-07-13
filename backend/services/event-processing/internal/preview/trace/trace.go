// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package trace reconstructs the slice-9e per-firing NODE TRACE: given a compiled canvas's node-id
// map (graph.NodeTracePlan) and one replay-preview firing, it reports what each canvas node did for
// that firing — "the source delivered, cond1 raised at 41.2, branch b2 blocked (its guard was
// false), act1 was skipped". The console renders this as a canvas overlay when an author clicks a
// firing on the preview timeline.
//
// It is a RECONSTRUCTION, not an engine tap (the design choice of spec §10.4; see graph/trace.go):
//   - the DETECT half is firing-granular — a firing exists only because the condition produced an
//     edge, so the source "delivered" and the condition "matched" (raised/resolved) by construction;
//   - the REACT half is recomputed by evaluating each branch's guard against the firing's own scalar
//     (value/series) with the SAME guard programs the REACT dispatcher runs (rules.BuildGuardProgram),
//     and applying the SAME edge routing the dispatcher applies (dispatcher.go): a rising edge gates
//     each action on its branch chain (raiseAlarm→raised, sendCommand→sent), a falling edge clears a
//     raiseAlarm UNCONDITIONALLY (the structural clear is never guarded — the load-bearing 9c
//     invariant) and does nothing for a sendCommand (a command has no falling-edge twin).
//
// So the trace matches what production would actually dispatch, without touching the DETECT engine.
package trace

import (
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
)

// Node kinds — the canvas node category a step belongs to.
const (
	KindSource    = "source"
	KindCondition = "condition"
	KindBranch    = "branch"
	KindAction    = "action"
)

// Dispositions — what a node did for one firing.
const (
	DispDelivered = "delivered" // a source: the telemetry reached the rule (always, for any firing)
	DispRaised    = "raised"    // a condition entered its satisfied state; OR a raiseAlarm action raised
	DispResolved  = "resolved"  // a condition left its satisfied state (the falling edge)
	DispPassed    = "passed"    // a branch: its guard was true, so the signal continued through it
	DispBlocked   = "blocked"   // a branch: its guard was false (or errored → fail closed), signal stopped
	DispSkipped   = "skipped"   // an upstream branch blocked, so this node was never reached
	DispSent      = "sent"      // a sendCommand action dispatched
	DispCleared   = "cleared"   // a raiseAlarm action cleared its contribution (a falling edge)
	DispInert     = "inert"     // a sendCommand on a falling edge — no side effect (no command twin)
)

// Step is one canvas node's disposition for one firing.
type Step struct {
	NodeID      string
	Kind        string
	Disposition string
	// Detail is optional human context: a guard-evaluation error reason on a blocked branch, or a
	// note explaining an unconditional clear. Empty when the disposition speaks for itself.
	Detail string
}

// Builder reconstructs traces for firings of ONE compiled canvas. It compiles the plan's branch
// guards ONCE up front (guards are stable across a preview's firings), then reuses them per firing —
// so a many-firing preview does not recompile a guard per firing. A guard that fails to build (a bug
// — it cleared the publish gate) is recorded and fails closed at every firing that would consult it.
type Builder struct {
	plan     graph.NodeTracePlan
	programs map[string]*rules.CompiledGuard // branch When source → compiled program (nil ⇒ build failed)
}

// NewBuilder pre-compiles the branch guards in plan. It never returns an error: a guard that will not
// build is stored as nil and treated as a blocking guard (fail closed) when a firing reaches it,
// mirroring how the REACT dispatcher fails closed on a guard it cannot build.
func NewBuilder(plan graph.NodeTracePlan) *Builder {
	b := &Builder{plan: plan, programs: map[string]*rules.CompiledGuard{}}
	for _, ap := range plan.Actions {
		for _, br := range ap.Branches {
			if _, seen := b.programs[br.When]; seen {
				continue
			}
			g, err := rules.BuildGuardProgram(br.When)
			if err != nil {
				b.programs[br.When] = nil // fail closed at eval time
				continue
			}
			b.programs[br.When] = g
		}
	}
	return b
}

// Firing is the subset of a preview firing the trace needs: its edge (raise vs resolve), the series
// the guard binds, and the scalar it evaluates against (Value valid only when HasValue).
type Firing struct {
	Raise    bool
	Series   string
	Value    float64
	HasValue bool
}

// Build reconstructs the node trace for one firing: the source + condition on the DETECT path, then
// each REACT action's branch chain evaluated against the firing. The steps are ordered source →
// condition → (per action, in the plan's deterministic order) its branches condition→action then the
// action itself — the order the console lays the path out.
func (b *Builder) Build(f Firing) []Step {
	steps := make([]Step, 0, 2+len(b.plan.Actions)*2)
	if b.plan.SourceID != "" {
		steps = append(steps, Step{NodeID: b.plan.SourceID, Kind: KindSource, Disposition: DispDelivered})
	}
	condDisp := DispRaised
	if !f.Raise {
		condDisp = DispResolved
	}
	steps = append(steps, Step{NodeID: b.plan.ConditionID, Kind: KindCondition, Disposition: condDisp})

	for _, ap := range b.plan.Actions {
		steps = append(steps, b.reactSteps(ap, f)...)
	}
	return steps
}

// reactSteps traces one action's branch chain + the action for a firing, honoring the dispatcher's
// edge routing.
func (b *Builder) reactSteps(ap graph.ActionPath, f Firing) []Step {
	// FALLING EDGE. The dispatcher never consults a guard on a resolve: a raiseAlarm clears its
	// contribution UNCONDITIONALLY (the structural clear — gating it would strand an alarm active
	// forever), and a sendCommand has no falling-edge twin. So the branches are not evaluated.
	if !f.Raise {
		out := make([]Step, 0, len(ap.Branches)+1)
		for _, br := range ap.Branches {
			out = append(out, Step{NodeID: br.NodeID, Kind: KindBranch, Disposition: DispSkipped, Detail: "not evaluated on a resolve"})
		}
		switch ap.Type {
		case string(rules.ActionRaiseAlarm):
			out = append(out, Step{NodeID: ap.NodeID, Kind: KindAction, Disposition: DispCleared, Detail: "the alarm clears unconditionally on resolve"})
		default: // sendCommand (or any non-alarm) — no effect on a falling edge
			out = append(out, Step{NodeID: ap.NodeID, Kind: KindAction, Disposition: DispInert, Detail: "a command has no resolve twin"})
		}
		return out
	}

	// RISING EDGE. Walk the branch chain; the signal continues only while each branch's guard passes.
	// Once one blocks, the rest are unreached and the action is skipped.
	out := make([]Step, 0, len(ap.Branches)+1)
	passed := true
	for _, br := range ap.Branches {
		if !passed {
			out = append(out, Step{NodeID: br.NodeID, Kind: KindBranch, Disposition: DispSkipped, Detail: "an earlier branch blocked"})
			continue
		}
		ok, detail := b.evalBranch(br, f)
		if ok {
			out = append(out, Step{NodeID: br.NodeID, Kind: KindBranch, Disposition: DispPassed})
			continue
		}
		out = append(out, Step{NodeID: br.NodeID, Kind: KindBranch, Disposition: DispBlocked, Detail: detail})
		passed = false
	}
	if !passed {
		out = append(out, Step{NodeID: ap.NodeID, Kind: KindAction, Disposition: DispSkipped, Detail: "a branch blocked the signal"})
		return out
	}
	switch ap.Type {
	case string(rules.ActionSendCommand):
		out = append(out, Step{NodeID: ap.NodeID, Kind: KindAction, Disposition: DispSent})
	default: // raiseAlarm
		out = append(out, Step{NodeID: ap.NodeID, Kind: KindAction, Disposition: DispRaised})
	}
	return out
}

// evalBranch reports whether a branch's guard passes for this firing, and a reason when it does not.
// It fails CLOSED exactly as the dispatcher does: a guard that would not build (recorded nil) or that
// errors at evaluation blocks the signal rather than passing it un-gated. detail is empty on a plain
// false (the guard simply did not match) and carries the error otherwise.
func (b *Builder) evalBranch(br graph.BranchStep, f Firing) (bool, string) {
	g := b.programs[br.When]
	if g == nil {
		return false, "the branch guard could not be compiled"
	}
	in := rules.GuardInput{Series: f.Series}
	if f.HasValue {
		v := f.Value
		in.Value = &v
	}
	ok, err := g.Eval(in)
	if err != nil {
		return false, "the branch guard errored during evaluation"
	}
	return ok, ""
}
