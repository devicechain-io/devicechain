// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"math"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// TestGenerateComparison pins the injection-safe rendering: a validated metric becomes a
// quoted, presence-guarded index compared with a double literal.
func TestGenerateComparison(t *testing.T) {
	got, err := generateComparison("temperature", OpGt, 30)
	if err != nil {
		t.Fatal(err)
	}
	want := `"temperature" in m && m["temperature"] > 30.0`
	if got != want {
		t.Fatalf("generated CEL:\n got %q\nwant %q", got, want)
	}
	// And it must actually compile + type-check against the shared env.
	if _, err := predicate.Compile(got, 1_000_000); err != nil {
		t.Fatalf("generated CEL should compile: %v", err)
	}
}

// TestGenerateComparisonRejectsInjection is the CEL-injection guard: a metric name that
// tries to break out of the string literal (or otherwise smuggle CEL syntax) is rejected
// by the ADR-042 token grammar before it can reach generated source.
func TestGenerateComparisonRejectsInjection(t *testing.T) {
	for _, bad := range []string{
		`temp"] || size(m) > 0 || m["x`, // quote/bracket breakout
		`temp || true`,                  // spaces + operators
		`m[0]`,                          // brackets
		`temp.celsius`,                  // dot
		`"`,                             // lone quote
		``,                              // empty
	} {
		if _, err := generateComparison(bad, OpGt, 1); err == nil {
			t.Errorf("metric %q should be rejected as an injection risk", bad)
		}
	}
}

// TestGenerateDynamicComparison pins the dynamic (attribute-sourced) rendering: both the
// metric and the attribute key become quoted, presence-guarded indexes, with the attribute
// guard emitted FIRST (short-circuit on the common never-set case).
func TestGenerateDynamicComparison(t *testing.T) {
	got, err := generateDynamicComparison("temperature", OpGt, "tempLimit")
	if err != nil {
		t.Fatal(err)
	}
	want := `"tempLimit" in attr && "temperature" in m && m["temperature"] > attr["tempLimit"]`
	if got != want {
		t.Fatalf("generated dynamic CEL:\n got %q\nwant %q", got, want)
	}
	// It must compile + type-check against the shared env (which declares attr as of schema v2).
	if _, err := predicate.Compile(got, 1_000_000); err != nil {
		t.Fatalf("generated dynamic CEL should compile: %v", err)
	}
	// And EVERY operator's dynamic form must fit the platform-default cost ceiling — a dynamic
	// comparison indexes two bounded maps, and eq/ne cost more than the ordered ops (the equality
	// overload's estimate), so the costliest form (eq/ne) is the one that must be pinned, not just
	// gt. If any dynamic comparison exceeded the ceiling, a gate-accepted structured rule could be
	// rejected by the engine's own compile — the parity this asserts against.
	for _, op := range []CompareOp{OpGt, OpGe, OpLt, OpLe, OpEq, OpNe} {
		src, err := generateDynamicComparison("temperature", op, "tempLimit")
		if err != nil {
			t.Fatalf("op %q: %v", op, err)
		}
		if _, err := predicate.Compile(src, defaultPredicateCostCeiling); err != nil {
			t.Fatalf("dynamic comparison with op %q must fit the default cost ceiling (%d): %v", op, defaultPredicateCostCeiling, err)
		}
	}
}

// TestGenerateDynamicComparisonRejectsInjection is the CEL-injection guard on BOTH author
// inputs — a breakout attempt in either the metric or the attribute key is rejected by the
// ADR-042 token grammar before it can reach generated source.
func TestGenerateDynamicComparisonRejectsInjection(t *testing.T) {
	bad := []string{
		`temp"] || size(m) > 0 || m["x`, // quote/bracket breakout
		`temp || true`,                  // spaces + operators
		`a.b`,                           // dot
		``,                              // empty
		strings.Repeat("a", 129),        // over core.MaxTokenLen (128)
	}
	for _, b := range bad {
		if _, err := generateDynamicComparison(b, OpGt, "tempLimit"); err == nil {
			t.Errorf("metric %q should be rejected as an injection/grammar risk", b)
		}
		if _, err := generateDynamicComparison("temperature", OpGt, b); err == nil {
			t.Errorf("attribute key %q should be rejected as an injection/grammar risk", b)
		}
	}
}

// TestDynamicComparisonEvalIsTotal proves the presence guards make evaluation total: the
// rule fires only when BOTH the metric is in the event AND the attribute is set for the
// device; a device that has not set the attribute (empty attr) is a clean non-match, never
// an evaluation error — the load-bearing property for the Duration-hold hazard.
func TestDynamicComparisonEvalIsTotal(t *testing.T) {
	src, err := generateDynamicComparison("temperature", OpGt, "tempLimit")
	if err != nil {
		t.Fatal(err)
	}
	pred, err := predicate.Compile(src, defaultPredicateCostCeiling)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   predicate.Input
		want bool
	}{
		{"over limit fires", predicate.Input{M: map[string]float64{"temperature": 40}, Attr: map[string]float64{"tempLimit": 30}}, true},
		{"under limit no fire", predicate.Input{M: map[string]float64{"temperature": 20}, Attr: map[string]float64{"tempLimit": 30}}, false},
		{"attr unset no fire", predicate.Input{M: map[string]float64{"temperature": 40}}, false},
		{"metric absent no fire", predicate.Input{Attr: map[string]float64{"tempLimit": 30}}, false},
		{"both empty no fire", predicate.Input{}, false},
	}
	for _, c := range cases {
		got, err := pred.Eval(c.in)
		if err != nil {
			t.Errorf("%s: unexpected eval error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: eval = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDoubleLiteral proves thresholds always render as CEL double literals (never bare
// ints, which would not type-check against a double measurement) and that non-finite
// thresholds are rejected.
func TestDoubleLiteral(t *testing.T) {
	cases := map[float64]string{
		30:    "30.0",
		30.5:  "30.5",
		-2:    "-2.0",
		0:     "0.0",
		0.001: "0.001",
	}
	for in, want := range cases {
		got, err := doubleLiteral(in)
		if err != nil {
			t.Errorf("doubleLiteral(%v): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("doubleLiteral(%v) = %q, want %q", in, got, want)
		}
	}
	if got, _ := doubleLiteral(1e21); !strings.ContainsAny(got, "eE.") {
		t.Errorf("large threshold %q must render as a double (with . or exponent)", got)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := doubleLiteral(bad); err == nil {
			t.Errorf("non-finite threshold %v must be rejected", bad)
		}
	}
}
