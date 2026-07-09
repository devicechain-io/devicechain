// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"errors"
	"testing"
	"time"
)

const testCeiling = 1_000_000

func mustCompile(t *testing.T, src string) *Predicate {
	t.Helper()
	p, err := Compile(src, testCeiling)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return p
}

// TestCompileAndEval covers the happy path: a boolean leaf compiles, reuses one program,
// and evaluates true/false against the declared event vocabulary.
func TestCompileAndEval(t *testing.T) {
	p := mustCompile(t, `"temperature" in m && m["temperature"] > 30.0`)
	hot := Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{"temperature": 35}}
	cold := Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{"temperature": 20}}
	if ok, err := p.Eval(hot); err != nil || !ok {
		t.Fatalf("hot should match: ok=%v err=%v", ok, err)
	}
	if ok, err := p.Eval(cold); err != nil || ok {
		t.Fatalf("cold should not match: ok=%v err=%v", ok, err)
	}
}

// TestEvalIsTotalOverMissingKey proves the presence-guarded form is total: an event
// missing the metric is a clean non-match, never an evaluation error — the property the
// generated comparison relies on.
func TestEvalIsTotalOverMissingKey(t *testing.T) {
	p := mustCompile(t, `"temperature" in m && m["temperature"] > 30.0`)
	in := Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{"humidity": 90}}
	ok, err := p.Eval(in)
	if err != nil {
		t.Fatalf("guarded eval over a missing key must not error: %v", err)
	}
	if ok {
		t.Fatal("missing metric must not match")
	}
}

// TestUnguardedMissingKeyErrors documents the raw-CEL escape hatch's contract: an
// unguarded index of an absent key is an evaluation error (the runtime counts it and
// treats it as a non-match), which is why the structured generator always emits the guard.
func TestUnguardedMissingKeyErrors(t *testing.T) {
	p := mustCompile(t, `m["temperature"] > 30.0`)
	in := Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{}}
	if _, err := p.Eval(in); err == nil {
		t.Fatal("unguarded index of a missing key should error")
	}
}

// TestNilMapsEvalCleanly proves a nil Anchors/M binds as an empty map so presence checks
// are false rather than erroring.
func TestNilMapsEvalCleanly(t *testing.T) {
	p := mustCompile(t, `"x" in m || "site" in anchors`)
	if ok, err := p.Eval(Input{Occurred: time.Unix(1, 0)}); err != nil || ok {
		t.Fatalf("nil maps should evaluate to a clean false: ok=%v err=%v", ok, err)
	}
}

// TestNonBooleanRejected proves a leaf that does not evaluate to a boolean is rejected at
// compile (a double-valued expression here).
func TestNonBooleanRejected(t *testing.T) {
	_, err := Compile(`m["x"]`, testCeiling)
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("want a CompileError for a non-boolean leaf, got %v", err)
	}
}

// TestTypeErrorRejected proves cel-go's type checker rejects a mistyped comparison at
// publish (double vs string).
func TestTypeErrorRejected(t *testing.T) {
	_, err := Compile(`m["x"] > "hot"`, testCeiling)
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("want a CompileError for a type error, got %v", err)
	}
}

// TestUndeclaredIdentifierRejected proves a leaf referencing a variable outside the
// declared vocabulary is rejected — the env is the whole contract.
func TestUndeclaredIdentifierRejected(t *testing.T) {
	_, err := Compile(`bogus > 1.0`, testCeiling)
	if err == nil {
		t.Fatal("an undeclared identifier must be rejected")
	}
}

// TestCostGate proves an expensive comprehension is rejected at a tight ceiling and
// accepted at a generous one — the fail-closed per-tenant cost gate.
func TestCostGate(t *testing.T) {
	const expensive = `m.all(k, m[k] > 0.0)`
	if _, err := Compile(expensive, 5); err == nil {
		t.Fatal("an expensive predicate must be rejected at a tight ceiling")
	} else {
		var cost *CostError
		if !errors.As(err, &cost) {
			t.Fatalf("want a CostError, got %v", err)
		}
	}
	if _, err := Compile(expensive, testCeiling); err != nil {
		t.Fatalf("the same predicate should pass a generous ceiling: %v", err)
	}
}
