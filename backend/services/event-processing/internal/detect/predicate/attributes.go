// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"errors"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
)

// ErrTrueWithoutAttributes is the refusal of a condition that is true on every event from every
// device holding none of the device attributes it reads, whatever that event carries. It is
// raised by rules.Compile — not by Compile here — and only for the rule kinds whose condition IS
// the alarm (threshold, duration): see TrueWithoutAttributes for why the kind decides.
//
// Why such a condition is refused rather than accepted as the author's business: a device has
// no entry in `attr` in FOUR situations, and only the first is what an author usually means by
// "not configured" —
//
//   - the attribute was never set;
//   - it was set to something that is not a number (the projection removes the key rather than
//     keeping a stale number);
//   - it was set with CLIENT scope, which is never projected into `attr`;
//   - it was set a moment ago and the change has not yet reached the detection engine.
//
// A condition that is true on all of those devices without looking at the event
// (`!("k" in attr) || …`, a bare `!("k" in attr)`, `size(attr) == 0`) raises an alarm for every
// one of them, fleet-wide, and each stays raised until that device's attribute arrives. That is
// the mistake this refuses. A condition that still tests the event — the fallback idiom
// `!("k" in attr) && m["t"] > 80.0` — is not refused: it is what an author writes on purpose.
//
// The message is console-surfaceable, so it says what to write instead.
var ErrTrueWithoutAttributes = errors.New(
	"the condition is true on every event from every device that has none of the attributes it reads, " +
		"whatever the event carries, so it would raise an alarm for each such device. A device has no value " +
		"for an attribute when it was never set, was set to something other than a number, was set with " +
		"CLIENT scope, or has not yet reached the detection engine. Guard the attribute positively " +
		"(\"k\" in attr && ...), or write the fallback as its own measurement test " +
		"(\"x\" in m && (\"k\" in attr ? m[\"x\"] > attr[\"k\"] : m[\"x\"] > 80.0))")

// TrueWithoutAttributes reports whether the leaf is DEFINITELY true when the device holds none of
// the attributes it reads, for every event and every device — its measurements, anchors, event
// time, position and device identity all held unknown. It is false for a leaf that never reads
// `attr` at all.
//
// It is a recorded FACT, not a refusal, because whether it is a mistake depends on what the leaf
// is FOR, which only the rule kind knows. Where the leaf is the alarm condition itself (threshold,
// duration) a pass-everything answer raises an alarm per device and rules.Compile refuses it. Where
// the leaf is an optional per-event GATE in front of a counting core (repeating, correlation, a
// count aggregate), a gate that passes every event from such a device is still narrower than the
// empty gate those kinds accept — `!("maint" in attr)` meaning "not in maintenance" is a
// legitimate filter there, and the counting core still has to trip.
//
// Two shapes are NOT detected, and both are narrower than the one this exists for:
//
//   - A per-key shape such as `"a" in attr && !("b" in attr)` (true for devices that have `a` but
//     lack `b`). Expressing "b absent, the others present with unknown values" is not possible in
//     cel-go: once any attribute pattern names `attr`, the bare `attr` identifier resolves to a
//     wholly unknown value (interpreter/attribute_patterns.go, the zero-qualifier case), so
//     `"a" in attr` becomes unknown rather than true. The only sound binding is `attr` empty and
//     known, which is the one used.
//   - A shape narrowed by identity, such as `!("k" in attr) && device == "d1"` or
//     `!("k" in attr) && "site" in anchors`. Identity is held unknown, so the result is unknown,
//     not true: the leaf is true for SOME devices lacking the attribute, not for every one.
//
// The analysis does not read the cost ceiling; it runs after the cost gate in Compile.
func (p *Predicate) TrueWithoutAttributes() bool { return p.trueWithoutAttributes }

// trueWhenAttributesAbsent reports whether the leaf is DEFINITELY true with `attr` bound to the
// empty map and every other variable (m, device, anchors, occurred, geo) held unknown via partial
// evaluation. It mirrors falseWhenMetricsAbsent with the roles swapped. Only a definite true
// counts: unknown means the leaf still depends on something about the event or the device. An
// evaluation ERROR (an unguarded `attr["k"]` on the empty map) also answers false, because the
// runtime already SKIPS such a leaf on those devices (see Eval) — a skip is not an alarm.
//
// Holding the non-attr variables unknown is the statement of intent ("for every event and every
// device"), but no test can tell it from leaving them unbound: an unbound variable is an
// evaluation error, error and unknown both answer false here, and `true || x` is true for either.
func trueWhenAttributesAbsent(env *cel.Env, ast *cel.Ast) (bool, error) {
	prg, err := env.Program(ast, cel.EvalOptions(cel.OptPartialEval))
	if err != nil {
		return false, err
	}
	act, err := cel.PartialVars(
		map[string]any{VarAttr: map[string]float64{}},
		cel.AttributePattern(VarM),
		cel.AttributePattern(VarDevice),
		cel.AttributePattern(VarAnchors),
		cel.AttributePattern(VarOccurred),
		cel.AttributePattern(VarGeo),
	)
	if err != nil {
		return false, err
	}
	out, _, err := prg.Eval(act)
	if err != nil {
		return false, nil
	}
	// A CEL unknown yields a non-bool Value(), so the comma-ok check rejects it with false.
	b, ok := out.Value().(bool)
	return ok && b, nil
}

// referencesAttr reports whether the leaf reads the `attr` variable at all, by any shape.
func referencesAttr(a *celast.AST) bool { return referencesIdent(a, VarAttr) }

// referencesIdent reports whether the type-checked AST names the identifier `name` anywhere.
func referencesIdent(a *celast.AST, name string) bool {
	if a == nil {
		return false
	}
	root := celast.NavigateAST(a)
	return len(celast.MatchDescendants(root, func(e celast.NavigableExpr) bool {
		return e.Kind() == celast.IdentKind && e.AsIdent() == name
	})) > 0
}
