// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustCompile(t *testing.T, src string) *Predicate {
	t.Helper()
	p, err := Compile(src)
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
	_, err := Compile(`m["x"]`)
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("want a CompileError for a non-boolean leaf, got %v", err)
	}
}

// TestTypeErrorRejected proves cel-go's type checker rejects a mistyped comparison at
// publish (double vs string).
func TestTypeErrorRejected(t *testing.T) {
	_, err := Compile(`m["x"] > "hot"`)
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("want a CompileError for a type error, got %v", err)
	}
}

// TestUndeclaredIdentifierRejected proves a leaf referencing a variable outside the
// declared vocabulary is rejected — the env is the whole contract.
func TestUndeclaredIdentifierRejected(t *testing.T) {
	_, err := Compile(`bogus > 1.0`)
	if err == nil {
		t.Fatal("an undeclared identifier must be rejected")
	}
}

// TestCostGate proves the gate compares the estimate against the ceiling it is given: the
// same comprehension is rejected at a tight ceiling and accepted at a generous one.
func TestCostGate(t *testing.T) {
	const expensive = `m.all(k, m[k] > 0.0)`
	if _, err := compile(expensive, 5); err == nil {
		t.Fatal("an expensive predicate must be rejected at a tight ceiling")
	} else {
		var cost *CostError
		if !errors.As(err, &cost) {
			t.Fatalf("want a CostError, got %v", err)
		}
	}
	if _, err := compile(expensive, 1_000_000); err != nil {
		t.Fatalf("the same predicate should pass a generous ceiling: %v", err)
	}
}

// TestCompileGatesAtThePlatformCeiling pins the exported entry point to the platform value.
// The ceiling is written as a literal, not as CostCeiling, so changing the platform value is
// a deliberate edit in two places rather than one that moves the test along with it.
func TestCompileGatesAtThePlatformCeiling(t *testing.T) {
	const expensive = `m.all(k, m[k] > 0.0)`
	_, err := Compile(expensive)
	var cost *CostError
	if !errors.As(err, &cost) {
		t.Fatalf("Compile(%q) = %v, want a *CostError at the platform ceiling", expensive, err)
	}
	if cost.Ceiling != 100 {
		t.Fatalf("Compile gated at a ceiling of %d, want the platform's 100", cost.Ceiling)
	}
	if cost.EstimatedMax <= 100 {
		t.Fatalf("the refusal reports an estimate of %d, which is within the ceiling it was refused at", cost.EstimatedMax)
	}
	// And a cheap leaf passes at the same ceiling, so this is a gate and not a wall.
	if _, err := Compile(`"t" in m && m["t"] > 1.0`); err != nil {
		t.Fatalf("a cheap leaf was refused at the platform ceiling: %v", err)
	}
}

// TestRuntimeCostLimitIsThePlatformCeiling pins the runtime backstop, which the static gate
// cannot: the estimator bounds a string pulled from anchors by its map hint, so this leaf
// estimates far under the ceiling while its actual cost grows with the value it is handed.
// A value that costs roughly half the ceiling must evaluate; one that costs roughly twice it
// must be cancelled by the Program's CostLimit. Together they hold the runtime limit to the
// same order as the publish-time ceiling, so raising or dropping it cannot pass unnoticed.
func TestRuntimeCostLimitIsThePlatformCeiling(t *testing.T) {
	const src = `"x" in anchors && anchors["x"].contains("y")`
	p := mustCompile(t, src)
	if p.CostMax() > 100 {
		t.Fatalf("%q estimates at %d; the probe needs a leaf the static gate admits", src, p.CostMax())
	}
	eval := func(n int) error {
		_, err := p.Eval(Input{Anchors: map[string]string{"x": strings.Repeat("a", n)}})
		return err
	}
	// contains costs about one unit per ten characters of the receiver.
	if err := eval(500); err != nil {
		t.Fatalf("a value costing about half the ceiling was refused at runtime: %v", err)
	}
	err := eval(2000)
	if err == nil {
		t.Fatal("a value costing about twice the ceiling evaluated; the runtime CostLimit is not the platform ceiling")
	}
	if !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("want a cost-limit cancellation, got %v", err)
	}
}
