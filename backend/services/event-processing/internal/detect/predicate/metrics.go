// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"sort"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// MetricRefs reports the measurement keys this predicate reads from the `m` map via a
// COMPILE-TIME CONSTANT string key — `m["temp"]`, `m.temp`, or `"temp" in m` — and whether that
// set is COMPLETE. Complete is true iff EVERY use of the `m` variable pins a constant key; it is
// false the moment the expression touches `m` opaquely (a dynamic key `m[expr]`, a whole-map
// operation like `size(m)`, or a comprehension `m.exists(k, …)`), because then the metrics the
// rule actually depends on cannot be determined statically.
//
// This answers only "which measurements does the leaf reference?" — NOT "is the leaf false when
// they are absent?". Those differ: `("temp" in m && m["temp"] > 80) || attr["x"] > 0` references
// only `temp` yet is true on a temp-less event when `attr["x"]` is set. ScopableMetrics is the
// safe basis for the feed scope; MetricRefs is exposed for diagnostics and tests.
func (p *Predicate) MetricRefs() (metrics []string, complete bool) {
	return p.metrics, p.metricsComplete
}

// ScopableMetrics returns the measurement keys the runtime may safely scope this leaf's feed on,
// and whether scoping is sound at all (review D4). It is sound ONLY when the reference set is
// complete AND non-empty AND the leaf is provably FALSE when none of those measurements are
// present — so an event carrying none of them cannot raise, and skipping it drops nothing. When
// ok is false the caller must feed every event (the "raw-CEL author owns totality" fallback).
//
// The soundness proof is the falseWhenMetricsAbsent partial evaluation done at compile: with a
// complete reference set the leaf reads `m` only through those keys, so an event lacking them is
// observationally `m={}`; if the leaf is definitely false under `m={}` for EVERY value of the
// other variables, no such event can raise. A leaf that can be true without its metrics (via
// `attr`, `device`, a negated presence, or a disjunction) fails this and is not scoped.
func (p *Predicate) ScopableMetrics() (metrics []string, ok bool) {
	if p.metricsComplete && len(p.metrics) > 0 && p.scopeSafe {
		return p.metrics, true
	}
	return nil, false
}

// falseWhenMetricsAbsent reports whether the leaf is DEFINITELY false when the measurement map is
// empty, evaluated over ALL values of the other variables (device, anchors, occurred, attr held
// unknown via partial evaluation). Only a definite `false` is safe to scope on: `true` means the
// leaf can raise without any measurement; `unknown` means it might; an evaluation ERROR (an
// unguarded `m["k"]` on empty `m`) is also not scoped — but such a leaf already error-SKIPS every
// off-metric event at runtime (PlanResult.EvalErrors), so feeding it everything is already safe and
// no raise is dropped. Restricting to definite-false keeps this sound without relying on cel-go's
// error-vs-unknown precedence in a mixed disjunction.
func falseWhenMetricsAbsent(env *cel.Env, ast *cel.Ast) (bool, error) {
	prg, err := env.Program(ast, cel.EvalOptions(cel.OptPartialEval))
	if err != nil {
		return false, err
	}
	act, err := cel.PartialVars(
		map[string]any{VarM: map[string]float64{}},
		cel.AttributePattern(VarDevice),
		cel.AttributePattern(VarAnchors),
		cel.AttributePattern(VarOccurred),
		cel.AttributePattern(VarAttr),
	)
	if err != nil {
		return false, err
	}
	out, _, err := prg.Eval(act)
	if err != nil {
		// The leaf errors with m empty for every assignment of the unknown vars; the runtime
		// treats an eval error as a skip, so not scoping (feed-everything + error-skip) is safe.
		return false, nil
	}
	// Definite false is the only safe-to-scope outcome. A CEL unknown result yields a non-bool
	// Value(), so the comma-ok check rejects true/unknown/non-bool alike.
	b, ok := out.Value().(bool)
	return ok && !b, nil
}

// metricRefs walks a type-checked predicate AST for constant-keyed reads of the `m` variable.
func metricRefs(a *celast.AST) (metrics []string, complete bool) {
	if a == nil {
		return nil, false
	}
	root := celast.NavigateAST(a)
	// Every reference to the `m` identifier, wherever it appears in the tree.
	idents := celast.MatchDescendants(root, func(e celast.NavigableExpr) bool {
		return e.Kind() == celast.IdentKind && e.AsIdent() == VarM
	})
	complete = true
	seen := make(map[string]struct{}, len(idents))
	for _, id := range idents {
		key, ok := constKeyForMUse(id)
		if !ok {
			// `m` is used in a way that does not pin a single constant key — the referenced
			// metric set is not statically knowable. Keep scanning (so a partial set is still
			// collected for diagnostics) but the result is not usable for feed scoping.
			complete = false
			continue
		}
		if _, dup := seen[key]; !dup {
			seen[key] = struct{}{}
			metrics = append(metrics, key)
		}
	}
	sort.Strings(metrics)
	return metrics, complete
}

// constKeyForMUse returns the constant metric key a single reference to the `m` identifier pins,
// and whether it pins one. It inspects the identifier's PARENT: a field select (`m.temp`, and the
// macro form `has(m.temp)`) names the key directly; a string-literal index (`m["temp"]`) or `in`
// test (`"temp" in m`) carries it as the sibling argument. Any other parent — a dynamic index, a
// whole-map call like `size(m)`, or a comprehension iterating `m` — does not pin a key.
func constKeyForMUse(id celast.NavigableExpr) (string, bool) {
	parent, ok := id.Parent()
	if !ok {
		return "", false
	}
	switch parent.Kind() {
	case celast.SelectKind:
		// `m.<field>`: the identifier can only be the operand, and the field name is the key.
		return parent.AsSelect().FieldName(), true
	case celast.CallKind:
		call := parent.AsCall()
		args := call.Args()
		if len(args) != 2 {
			return "", false
		}
		switch call.FunctionName() {
		case operators.Index, operators.OptIndex:
			// `m[<key>]`: m is the container (arg 0), the key is arg 1 (must be a string literal).
			if sameNode(args[0], id) {
				return stringLiteral(args[1])
			}
		case operators.In, operators.OldIn:
			// `<key> in m`: m is the container (arg 1), the key is arg 0 (must be a string literal).
			if sameNode(args[1], id) {
				return stringLiteral(args[0])
			}
		}
	}
	return "", false
}

// sameNode reports whether an argument expression IS the given identifier node (by AST id).
func sameNode(arg celast.Expr, id celast.NavigableExpr) bool { return arg.ID() == id.ID() }

// stringLiteral returns the Go string value of a string-literal expression.
func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}
