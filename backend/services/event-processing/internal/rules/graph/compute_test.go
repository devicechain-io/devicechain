// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// celCond is a condition node whose leaf is a RAW-CEL expression (the only leaf a compute can feed).
func celCond(id, cel string) Node {
	return Node{ID: id, Type: NodeThreshold, Config: cfg(map[string]interface{}{
		"name": "c", "when": map[string]interface{}{"cel": cel},
	})}
}

// compute is a compute node with a name + value expression.
func compute(id, name, expr string) Node {
	return Node{ID: id, Type: NodeCompute, Config: cfg(map[string]interface{}{"name": name, "expr": expr})}
}

// TestComputeFoldsIntoCelLeaf: a compute wired into a condition's value input folds onto its raw-CEL
// leaf as a cel.bind, and the COMPILED predicate evaluates the composed logic correctly.
func TestComputeFoldsIntoCelLeaf(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{
			src("s"),
			compute("cmp", "tempF", `m["tempC"] * 1.8 + 32.0`),
			celCond("c", `"tempC" in m && tempF > 100.0`),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "cmp:value", To: "c:value"},
	))
	if !strings.Contains(lr.Definition, "cel.bind(tempF") {
		t.Fatalf("expected the folded leaf to carry a cel.bind for tempF; got %s", lr.Definition)
	}
	// 40°C = 104°F > 100 → raise; 30°C = 86°F → no.
	hot, err := lr.Compiled.Predicate.Eval(predicate.Input{M: map[string]float64{"tempC": 40}})
	if err != nil || !hot {
		t.Fatalf("eval(40°C) = %v, err %v; want true", hot, err)
	}
	cool, err := lr.Compiled.Predicate.Eval(predicate.Input{M: map[string]float64{"tempC": 30}})
	if err != nil || cool {
		t.Fatalf("eval(30°C) = %v, err %v; want false", cool, err)
	}
}

// TestComputeFoldsIntoBranchGuard: a compute wired into a branch's value input folds onto the branch
// guard (guard env: value/hasValue/series), and the action carries the composed cel.bind guard.
func TestComputeFoldsIntoBranchGuard(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{
			src("s"), hotThreshold("c"),
			compute("cmp", "doubled", `value * 2.0`),
			branch("b", "doubled > 100.0"),
			raiseAlarm("a", "k"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "b:in"},
		Edge{From: "cmp:value", To: "b:value"},
		Edge{From: "b:out", To: "a:in"},
	))
	if len(lr.Rule.Actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(lr.Rule.Actions))
	}
	g := lr.Rule.Actions[0].Guard
	if !strings.Contains(g, "cel.bind(doubled") {
		t.Fatalf("expected the action guard to carry a cel.bind for doubled; got %q", g)
	}
	// The composed guard must actually compile + evaluate as a guard.
	prog, err := rules.BuildGuardProgram(g)
	if err != nil {
		t.Fatalf("composed guard did not build: %v", err)
	}
	v := 60.0
	ok, err := prog.Eval(rules.GuardInput{Value: &v}) // 120 > 100 → true
	if err != nil || !ok {
		t.Fatalf("guard eval(60) = %v, err %v; want true", ok, err)
	}
}

// TestComputeIntoStructuredLeafRejected: a compute cannot feed a structured (metric/op/threshold)
// leaf — there is no expression to reference the computed value — so it fails closed.
func TestComputeIntoStructuredLeafRejected(t *testing.T) {
	_, err := Compile(canvas(
		[]Node{src("s"), compute("cmp", "x", "1.0"), hotThreshold("c")},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "cmp:value", To: "c:value"},
	), profile, rules.DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "CEL predicate") {
		t.Fatalf("expected a structured-leaf rejection, got %v", err)
	}
}

// TestComputeNameRejects: an invalid, leading-digit, dashed, or reserved compute name fails closed.
func TestComputeNameRejects(t *testing.T) {
	for _, name := range []string{"bad-name", "2x", "m", "value", ""} {
		_, err := Compile(canvas(
			[]Node{src("s"), compute("cmp", name, "1.0"), celCond("c", "device != \"\"")},
			Edge{From: "s:out", To: "c:in"},
			Edge{From: "cmp:value", To: "c:value"},
		), profile, rules.DefaultLimits())
		if err == nil {
			t.Fatalf("expected rejection of compute name %q", name)
		}
	}
}

// TestComputeEmptyExprRejected: a compute with no expression fails closed.
func TestComputeEmptyExprRejected(t *testing.T) {
	_, err := Compile(canvas(
		[]Node{src("s"), compute("cmp", "x", "  "), celCond("c", "x > 0.0")},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "cmp:value", To: "c:value"},
	), profile, rules.DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("expected an empty-expr rejection, got %v", err)
	}
}

// TestDuplicateComputeNamesRejected: two computes with the same name feeding one consumer collide as
// one cel.bind variable, so the compiler rejects them rather than silently dropping one.
func TestDuplicateComputeNamesRejected(t *testing.T) {
	_, err := Compile(canvas(
		[]Node{
			src("s"),
			compute("c1", "x", "1.0"), compute("c2", "x", "2.0"),
			celCond("c", "x > 0.0"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c1:value", To: "c:value"},
		Edge{From: "c2:value", To: "c:value"},
	), profile, rules.DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "distinct name") {
		t.Fatalf("expected a duplicate-name rejection, got %v", err)
	}
}

// TestUnwiredComputeValidated: a poisoned UNWIRED compute (invalid name) still rides in the stored
// AuthoringGraph sidecar, so it is caught up front — not left to surface when someone wires it later.
func TestUnwiredComputeValidated(t *testing.T) {
	_, err := Compile(canvas(
		[]Node{src("s"), hotThreshold("c"), compute("cmp", "bad-name", "1.0")},
		Edge{From: "s:out", To: "c:in"},
	), profile, rules.DefaultLimits())
	if err == nil {
		t.Fatal("expected an unwired poisoned compute to be rejected up front")
	}
}

// TestComputeFoldDeterministic: two computes feeding one leaf compose in a fixed (name-sorted) order,
// so the stored definition bytes are stable run-to-run — the byte-identity discipline (§3.2).
func TestComputeFoldDeterministic(t *testing.T) {
	def := canvas(
		[]Node{
			src("s"),
			compute("cA", "bbb", "2.0"), compute("cB", "aaa", "1.0"),
			celCond("c", "aaa + bbb > 0.0"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "cA:value", To: "c:value"},
		Edge{From: "cB:value", To: "c:value"},
	)
	first := compileOne(t, def).Definition
	second := compileOne(t, def).Definition
	if first != second {
		t.Fatalf("compute fold is not deterministic:\n %s\n %s", first, second)
	}
	// aaa (sorted first) must be the OUTER bind.
	if strings.Index(first, "cel.bind(aaa") > strings.Index(first, "cel.bind(bbb") {
		t.Fatalf("expected name-sorted fold (aaa outermost); got %s", first)
	}
}

// TestValueEdgeIntoNonValueInputRejected: a value edge into a node with no value input port (an
// action) is rejected by the port type check — value only flows into a condition or a branch.
func TestValueEdgeIntoNonValueInputRejected(t *testing.T) {
	_, err := Compile(canvas(
		[]Node{
			src("s"), hotThreshold("c"),
			compute("cmp", "x", "1.0"),
			raiseAlarm("a", "k"),
		},
		Edge{From: "s:out", To: "c:in"},
		Edge{From: "c:signal", To: "a:in"},
		Edge{From: "cmp:value", To: "a:in"},
	), profile, rules.DefaultLimits())
	if err == nil {
		t.Fatal("expected a value edge into an action's signal input to be rejected")
	}
}
