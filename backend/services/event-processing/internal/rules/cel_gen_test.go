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
