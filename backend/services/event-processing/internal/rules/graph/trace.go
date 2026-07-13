// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

// This file carries the slice-9e node-trace PLAN: the authoring-only map from a compiled canvas
// back to its node ids, so the replay preview can reconstruct — per firing — which canvas node did
// what ("this event reached cond1, passed; branch b2 blocked; act1 fired"). It is NEVER part of the
// runtime rule (device-management stores only the flat rules.Rule Definition, ADR-045 discipline);
// it is built during Compile and handed to the preview harness alongside the compiled rule.
//
// THE DESIGN CHOICE (spec §10.4). The trace is reconstructed per FIRING from the firing's carried
// scalar + this static plan, NOT by tapping the keyed-streaming DETECT engine. So:
//   - the DETECT half (source → condition) is represented at FIRING granularity: a firing exists
//     iff the condition produced an edge, so on any firing the source "delivered" and the condition
//     "matched". A non-firing event has no trace (the §10.4 "cap to firing events" decision — a full
//     per-event trace over 24h is the deferred, expensive option);
//   - the REACT half (branch → action) is reconstructed by evaluating each branch's guard against the
//     firing scalar with the SAME guard programs the REACT dispatcher runs, and applying the same
//     edge-routing (a resolved edge clears raiseAlarm unconditionally and sends nothing — ADR-057 /
//     dispatcher.go), so the trace matches what production would actually dispatch.
// This keeps the correctness-critical DETECT engine completely untouched (the 9d isolation contract).

// NodeTracePlan describes one compiled canvas condition's node structure for the per-firing trace:
// the source + condition nodes on the DETECT path (always traversed by any firing), and the ordered
// branch chain from the condition to each REACT action. Built by Compile; consumed only by preview.
type NodeTracePlan struct {
	// SourceID is the source node feeding the condition. Empty only defensively (a compiled rule
	// always has exactly one source), in which case the trace omits the source step.
	SourceID string
	// ConditionID is the condition node (== LoweredRule.NodeID) whose edge is the firing.
	ConditionID string
	// Actions is the REACT chains fanning off this condition, one per action node, in the same
	// deterministic (action-id-sorted) order the rule's Actions carry.
	Actions []ActionPath
}

// ActionPath is one REACT action node and the ordered branch chain gating it from the condition.
type ActionPath struct {
	// NodeID is the action node's canvas id.
	NodeID string
	// Type is "raiseAlarm" | "sendCommand" (rules.ActionType) — it selects the trace disposition
	// (raised/sent on a rising edge, cleared/inert on a falling one).
	Type string
	// Branches are the branch nodes on the signal path from the condition to this action, in
	// condition→action order — the order they gate in. A firing's trace evaluates each in turn and
	// stops at the first that blocks (its guard is false), matching how the composed guard short-
	// circuits the action.
	Branches []BranchStep
}

// BranchStep is one branch node on an action's path: the node id and the CEL guard the trace
// evaluates against the firing scalar to decide whether the signal passed through it.
type BranchStep struct {
	NodeID string
	When   string
}
