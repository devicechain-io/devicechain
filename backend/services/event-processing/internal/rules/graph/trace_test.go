// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"reflect"
	"testing"
)

// TestTracePlanSourceAndCondition: even a bare source→condition canvas (no REACT) yields a trace
// plan naming the source and condition nodes, so a firing can always be attributed to them.
func TestTracePlanSourceAndCondition(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{src("s"), hotThreshold("c")},
		Edge{From: "s:out", To: "c:in"},
	))
	if lr.Trace.SourceID != "s" || lr.Trace.ConditionID != "c" {
		t.Fatalf("trace plan source/condition = %q/%q, want s/c", lr.Trace.SourceID, lr.Trace.ConditionID)
	}
	if len(lr.Trace.Actions) != 0 {
		t.Fatalf("want no action paths, got %d", len(lr.Trace.Actions))
	}
}

// TestTracePlanBranchChainOrder: a condition→b1→b2→action canvas records the action path with its
// branch chain in condition→action order (b1 then b2) — the order the trace evaluates them in.
func TestTracePlanBranchChainOrder(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{src("s"), hotThreshold("c"), branch("b1", "value > 0.0"), branch("b2", `series == "d"`), raiseAlarm("a", "k")},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b1:in"},
		Edge{From: "b1:out", To: "b2:in"},
		Edge{From: "b2:out", To: "a:in"},
	))
	if len(lr.Trace.Actions) != 1 {
		t.Fatalf("want 1 action path, got %d", len(lr.Trace.Actions))
	}
	ap := lr.Trace.Actions[0]
	if ap.NodeID != "a" || ap.Type != "raiseAlarm" {
		t.Fatalf("action path node/type = %q/%q, want a/raiseAlarm", ap.NodeID, ap.Type)
	}
	want := []BranchStep{{NodeID: "b1", When: "value > 0.0"}, {NodeID: "b2", When: `series == "d"`}}
	if !reflect.DeepEqual(ap.Branches, want) {
		t.Fatalf("branch chain = %+v, want %+v", ap.Branches, want)
	}
}

// TestTracePlanMultiActionSorted: two actions fanning off one condition are recorded in id-sorted
// order (matching the rule's Actions order), each with its own branch chain.
func TestTracePlanMultiActionSorted(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{
			src("s"), hotThreshold("c"),
			branch("b", "value > 50.0"),
			raiseAlarm("a2", "k2"),
			raiseAlarm("a1", "k1"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b:in"},
		Edge{From: "b:out", To: "a1:in"},    // a1 gated by branch b
		Edge{From: "c:signal", To: "a2:in"}, // a2 wired straight through
	))
	if len(lr.Trace.Actions) != 2 {
		t.Fatalf("want 2 action paths, got %d", len(lr.Trace.Actions))
	}
	if lr.Trace.Actions[0].NodeID != "a1" || lr.Trace.Actions[1].NodeID != "a2" {
		t.Fatalf("action order = %q,%q, want a1,a2 (id-sorted)", lr.Trace.Actions[0].NodeID, lr.Trace.Actions[1].NodeID)
	}
	if len(lr.Trace.Actions[0].Branches) != 1 || lr.Trace.Actions[0].Branches[0].NodeID != "b" {
		t.Fatalf("a1 should be gated by branch b, got %+v", lr.Trace.Actions[0].Branches)
	}
	if len(lr.Trace.Actions[1].Branches) != 0 {
		t.Fatalf("a2 wires straight through — want no branches, got %+v", lr.Trace.Actions[1].Branches)
	}
}
