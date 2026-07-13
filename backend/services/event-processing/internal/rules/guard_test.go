// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import "testing"

func f64(v float64) *float64 { return &v }

// TestCompileGuard is the publish-time gate: a valid boolean over the guard vocabulary compiles; a
// non-boolean, an undeclared identifier, and a guard over the cost ceiling all reject (fail closed).
func TestCompileGuard(t *testing.T) {
	const ceiling = 100
	ok := []string{
		`value > 100.0`,
		`hasValue && value > 100.0`,
		`series == "device-1"`,
		`!hasValue || value <= 0.0`,
	}
	for _, src := range ok {
		if _, err := CompileGuard(src, ceiling); err != nil {
			t.Errorf("CompileGuard(%q): unexpected error: %v", src, err)
		}
	}
	bad := []struct {
		name string
		src  string
	}{
		{"non-boolean", `value + 1`},
		{"undeclared var (the resolved-event map is not in scope)", `m["tempC"] > 1`},
		{"undeclared var attr", `attr["x"] > 1`},
		{"parse error", `value >`},
	}
	for _, tc := range bad {
		if _, err := CompileGuard(tc.src, ceiling); err == nil {
			t.Errorf("CompileGuard(%q) [%s]: expected an error", tc.src, tc.name)
		}
	}
	// The cost gate: a real guard estimates a positive cost, so a zero ceiling rejects it (never
	// "unlimited" — the ADR-023 fail-safe the caller must respect by flooring before this call).
	if _, err := CompileGuard(`value > 100.0`, 0); err == nil {
		t.Error("CompileGuard with a zero ceiling should reject a cost-bearing guard")
	}
}

// TestGuardEval proves the value/hasValue/series binding: a nil value binds hasValue=false and
// value=0.0, so a bare value comparison is a clean false rather than an error.
func TestGuardEval(t *testing.T) {
	g, err := BuildGuardProgram(`hasValue && value > 100.0`)
	if err != nil {
		t.Fatalf("BuildGuardProgram: %v", err)
	}
	cases := []struct {
		val  *float64
		want bool
	}{
		{f64(150), true},
		{f64(50), false},
		{nil, false}, // hasValue is false → the whole guard is false, no evaluation error
	}
	for _, tc := range cases {
		got, err := g.Eval(GuardInput{Value: tc.val})
		if err != nil {
			t.Fatalf("Eval(%v): %v", tc.val, err)
		}
		if got != tc.want {
			t.Errorf("Eval(value=%v) = %v, want %v", tc.val, got, tc.want)
		}
	}

	series, err := BuildGuardProgram(`series == "d1"`)
	if err != nil {
		t.Fatalf("BuildGuardProgram(series): %v", err)
	}
	for _, tc := range []struct {
		s    string
		want bool
	}{{"d1", true}, {"d2", false}} {
		got, err := series.Eval(GuardInput{Series: tc.s})
		if err != nil {
			t.Fatalf("Eval(series=%q): %v", tc.s, err)
		}
		if got != tc.want {
			t.Errorf("Eval(series=%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestGuardNonBooleanBuildRejected: BuildGuardProgram (the dispatch path, no cost gate) still
// rejects a non-boolean, so a forged/hand-edited definition that slipped a non-boolean guard past a
// missing publish gate cannot reach evaluation.
func TestGuardNonBooleanBuildRejected(t *testing.T) {
	if _, err := BuildGuardProgram(`value + 1`); err == nil {
		t.Error("BuildGuardProgram should reject a non-boolean guard")
	}
}
