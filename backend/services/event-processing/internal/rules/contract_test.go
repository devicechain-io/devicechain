// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// TestDecodeRejectsUnknownFields proves the strict decoder fails closed on an unknown key
// — the fail-open hazard where a mistyped `when_` silently drops the author's gate and the
// rule over-fires. A plain json.Unmarshal would accept it.
func TestDecodeRejectsUnknownFields(t *testing.T) {
	// A repeating rule with a typo'd gate key: strict decode must reject, not silently
	// discard the gate.
	bad := `{"name":"x","type":"repeating","when_":{"cel":"m[\"t\"]>1.0"},"count":3,"window":"5m"}`
	if _, err := Decode([]byte(bad)); err == nil {
		t.Fatal("Decode must reject an unknown field (the dropped-gate fail-open hazard)")
	}
	// A well-formed rule must still decode and compile.
	good := `{"id":"r","name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":30}}`
	r, err := Decode([]byte(good))
	if err != nil {
		t.Fatalf("Decode of a valid rule: %v", err)
	}
	if _, err := Compile(r, testLimits); err != nil {
		t.Fatalf("decoded rule should compile: %v", err)
	}
	// Trailing content after the object is rejected.
	if _, err := Decode([]byte(good + `{}`)); err == nil {
		t.Fatal("Decode must reject trailing content")
	}
	// thresholdAttr is a KNOWN field (schema v2): a dynamic-threshold rule must decode and compile.
	dyn := `{"id":"r","name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","thresholdAttr":"tempLimit"}}`
	dr, err := Decode([]byte(dyn))
	if err != nil {
		t.Fatalf("Decode of a dynamic-threshold rule: %v", err)
	}
	if _, err := Compile(dr, testLimits); err != nil {
		t.Fatalf("decoded dynamic-threshold rule should compile: %v", err)
	}
}

// TestGateMetricExposesFeedScope proves the compiler surfaces the relevance metric the
// runtime needs to scope a rule's event feed (the metric-scoped feed contract): a
// structured leaf reports its GateMetric, a value kind reports ValueMetric, and a raw /
// match-every / device-scoped kind reports neither.
func TestGateMetricExposesFeedScope(t *testing.T) {
	cases := []struct {
		name      string
		rule      Rule
		wantGate  string
		wantValue string
	}{
		{"structured threshold", Rule{ID: "r", Name: "n", Type: TypeThreshold,
			When: Condition{Metric: "temp", Op: OpGt, Threshold: ptr(30)}}, "temp", ""},
		{"structured duration", Rule{ID: "r", Name: "n", Type: TypeDuration, Hold: Duration(time.Minute),
			When: Condition{Metric: "temp", Op: OpGt, Threshold: ptr(30)}}, "temp", ""},
		{"dynamic threshold gates on metric", Rule{ID: "r", Name: "n", Type: TypeThreshold,
			When: Condition{Metric: "temp", Op: OpGt, ThresholdAttr: "tempLimit"}}, "temp", ""},
		{"raw threshold has no gate", Rule{ID: "r", Name: "n", Type: TypeThreshold,
			When: Condition{CEL: `"temp" in m && m["temp"] > 30.0`}}, "", ""},
		{"deltaRate uses value metric", Rule{ID: "r", Name: "n", Type: TypeDeltaRate,
			Metric: "count", Op: OpGt, Threshold: ptr(1)}, "", "count"},
		{"absence is device-scoped", Rule{ID: "r", Name: "n", Type: TypeAbsence, Ttl: Duration(time.Minute)}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := Compile(tc.rule, testLimits)
			if err != nil {
				t.Fatal(err)
			}
			if cr.GateMetric != tc.wantGate || cr.ValueMetric != tc.wantValue {
				t.Fatalf("gate=%q value=%q, want gate=%q value=%q", cr.GateMetric, cr.ValueMetric, tc.wantGate, tc.wantValue)
			}
		})
	}
}

// TestDurationCancelledByMetricAbsentEvent documents WHY the metric-scoped feed contract
// exists: feeding a Duration rule an event that lacks its gate metric evaluates the leaf to
// false and CANCELS the hold. The runtime (slice 4) must therefore feed a Duration rule
// only events carrying GateMetric; this test pins the hazard so the contract is not lost.
func TestDurationCancelledByMetricAbsentEvent(t *testing.T) {
	cr, err := Compile(Rule{ID: "du", Name: "stuck", Type: TypeDuration, Hold: Duration(10 * time.Second),
		When: Condition{Metric: "temp", Op: OpGt, Threshold: ptr(30)}}, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if cr.GateMetric != "temp" {
		t.Fatalf("expected GateMetric temp for the feed-scope hook, got %q", cr.GateMetric)
	}
	e := core.NewEngine([]core.Rule{cr.Core}, 0)
	feed := func(seq uint64, sec int, m map[string]float64) {
		ev, err := cr.BuildEvent(seq, "dev1", "dev1", predicate.Input{Occurred: at(sec), M: m})
		if err != nil {
			t.Fatal(err)
		}
		e.ProcessEvent(ev)
	}
	feed(1, 0, map[string]float64{"temp": 35})    // arms the hold, deadline @10
	feed(2, 3, map[string]float64{"battery": 80}) // metric absent → leaf false → CANCELS
	e.Advance(at(11))
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("an unscoped metric-absent event cancelled the hold as expected, but the rule still fired: %+v — "+
			"if this ever passes the feed scoping changed", d)
	}
	// The same rule, fed only its gate metric (what slice 4 must do), fires correctly.
	e2 := core.NewEngine([]core.Rule{cr.Core}, 0)
	ev1, _ := cr.BuildEvent(1, "dev1", "dev1", predicate.Input{Occurred: at(0), M: map[string]float64{"temp": 35}})
	e2.ProcessEvent(ev1)
	e2.Advance(at(11))
	if d := e2.Drain(); len(d) != 1 {
		t.Fatalf("metric-scoped feed should fire once at the deadline, got %+v", d)
	}
}

// TestCompileErrorAnchoring proves a leaf failure is wrapped so the console gets the rule
// id + `when` field anchor, while errors.As still reaches the underlying predicate error
// (a caller can distinguish a cost rejection from a type error).
func TestCompileErrorAnchoring(t *testing.T) {
	// A raw-CEL leaf that trips the cost ceiling.
	_, err := Compile(Rule{ID: "r7", Name: "big", Type: TypeThreshold,
		When: Condition{CEL: `m.all(k, m[k] > 0.0)`}}, Limits{PredicateCostCeiling: 5})
	if err == nil {
		t.Fatal("expected a cost rejection")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RuleID != "r7" || ve.Field != "when" {
		t.Fatalf("want a ValidationError anchored to rule r7/when, got %v", err)
	}
	var ce *predicate.CostError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As should reach the underlying CostError, got %v", err)
	}
}
