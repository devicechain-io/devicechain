// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// hotThreshold is the standard `tempC > 30` critical condition used by the branch tests.
func hotThreshold(id string) Node {
	return Node{ID: id, Type: NodeThreshold, Config: cfg(map[string]interface{}{
		"name": "hot", "severity": "critical",
		"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
	})}
}

func branch(id, when string) Node {
	return Node{ID: id, Type: NodeBranch, Config: cfg(map[string]interface{}{"when": when})}
}

func raiseAlarm(id, key string) Node {
	return Node{ID: id, Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm", "alarmKey": key})}
}

// TestBranchGuardByteIdentity: a condition→branch→action lowers to a rule whose single action
// carries the branch predicate as its Guard, byte-identical to the equivalent hand-written form rule
// (§3.2 — the byte-identity contract holds for guarded rules).
func TestBranchGuardByteIdentity(t *testing.T) {
	def := canvas(
		[]Node{src("s"), hotThreshold("c"), branch("b", "value > 100.0"), raiseAlarm("a", "overheat")},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b:in"},
		Edge{From: "b:out", To: "a:in"},
	)
	want := rules.Rule{
		Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
		When: rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
		Actions: []rules.Action{{
			Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"},
			Guard: "value > 100.0",
		}},
	}
	got := compileOne(t, def)
	if !reflect.DeepEqual(got.Rule, want) {
		t.Fatalf("lowered rule mismatch\n got: %+v\nwant: %+v", got.Rule, want)
	}
	wantBytes, _ := json.Marshal(want)
	if got.Definition != string(wantBytes) {
		t.Fatalf("definition bytes mismatch\n got: %s\nwant: %s", got.Definition, wantBytes)
	}
	// And the stored definition decodes back through the DETECT decoder (which accepts the guard).
	if _, err := rules.Decode([]byte(got.Definition)); err != nil {
		t.Fatalf("rules.Decode(definition with guard): %v", err)
	}
}

// TestChainedBranchesCompose: condition→b1→b2→action composes the two predicates with && in
// condition→action order.
func TestChainedBranchesCompose(t *testing.T) {
	def := canvas(
		[]Node{src("s"), hotThreshold("c"), branch("b1", "value > 0.0"), branch("b2", `series == "d"`), raiseAlarm("a", "k")},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b1:in"},
		Edge{From: "b1:out", To: "b2:in"},
		Edge{From: "b2:out", To: "a:in"},
	)
	got := compileOne(t, def)
	if len(got.Rule.Actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(got.Rule.Actions))
	}
	if guard := got.Rule.Actions[0].Guard; guard != `(value > 0.0) && (series == "d")` {
		t.Fatalf("composed guard = %q, want condition→action order", guard)
	}
}

// TestBranchFanout: one condition fanning into two branches → two independently-guarded actions on
// ONE rule (the condition owns both). Actions are ordered by node id, so the guards are deterministic.
func TestBranchFanout(t *testing.T) {
	def := canvas(
		[]Node{
			src("s"), hotThreshold("c"),
			branch("b1", "value > 100.0"), branch("b2", "value <= 100.0"),
			raiseAlarm("a1", "severe"), raiseAlarm("a2", "mild"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b1:in"},
		Edge{From: "c:signal", To: "b2:in"},
		Edge{From: "b1:out", To: "a1:in"},
		Edge{From: "b2:out", To: "a2:in"},
	)
	got := compileOne(t, def)
	if len(got.Rule.Actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(got.Rule.Actions))
	}
	// Ordered by action node id: a1 then a2.
	if got.Rule.Actions[0].RaiseAlarm.AlarmKey != "severe" || got.Rule.Actions[0].Guard != "value > 100.0" {
		t.Fatalf("action[0] = %+v", got.Rule.Actions[0])
	}
	if got.Rule.Actions[1].RaiseAlarm.AlarmKey != "mild" || got.Rule.Actions[1].Guard != "value <= 100.0" {
		t.Fatalf("action[1] = %+v", got.Rule.Actions[1])
	}
}

// TestBranchRejects covers the fail-closed branch constructs, each anchored to a surfaced node.
func TestBranchRejects(t *testing.T) {
	cases := []struct {
		name       string
		def        CanvasDefinition
		wantNodeID string
	}{
		{
			name: "branch with an empty predicate",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), branch("b", ""), raiseAlarm("a", "k")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "b:in"},
				Edge{From: "b:out", To: "a:in"},
			),
			wantNodeID: "b",
		},
		{
			name: "branch with an invalid guard (undeclared identifier)",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), branch("b", `m["tempC"] > 1`), raiseAlarm("a", "k")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "b:in"},
				Edge{From: "b:out", To: "a:in"},
			),
			wantNodeID: "b",
		},
		{
			name: "branch with no input",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), branch("b", "value > 1.0"), raiseAlarm("a", "k")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "b:out", To: "a:in"}, // b has no incoming signal
			),
			wantNodeID: "b",
		},
		{
			name: "action fed by two branches (cross-signal join)",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), branch("b1", "value > 1.0"), branch("b2", "value > 2.0"), raiseAlarm("a", "k")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "b1:in"},
				Edge{From: "c:signal", To: "b2:in"},
				Edge{From: "b1:out", To: "a:in"},
				Edge{From: "b2:out", To: "a:in"}, // a fed by two signals
			),
			wantNodeID: "a",
		},
		{
			name: "unwired poisoned branch (empty predicate) still rejected up front",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), raiseAlarm("a", "k"), branch("orphan", "")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			wantNodeID: "orphan",
		},
		{
			name: "signal into a branch's non-existent port",
			def: canvas(
				[]Node{src("s"), hotThreshold("c"), branch("b", "value > 1.0"), raiseAlarm("a", "k")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "b:nope"}, // branch has input port "in", not "nope"
				Edge{From: "b:out", To: "a:in"},
			),
			wantNodeID: "b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.def, profile, rules.DefaultLimits())
			if err == nil {
				t.Fatal("expected a compile error, got nil")
			}
			var ce *CompileError
			if !errors.As(err, &ce) {
				t.Fatalf("expected *CompileError, got %T: %v", err, err)
			}
			if ce.Diagnostics[0].NodeID != tc.wantNodeID {
				t.Fatalf("diagnostic node id = %q, want %q (msg: %s)", ce.Diagnostics[0].NodeID, tc.wantNodeID, ce.Diagnostics[0].Message)
			}
		})
	}
}
