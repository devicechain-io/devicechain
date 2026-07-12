// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"sort"

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
// The runtime uses this to metric-scope the feed for a raw-CEL leaf the structured lowering could
// not scope (review D4): a raw-CEL threshold/duration rule is fed only events carrying a metric it
// references, so unrelated telemetry (a battery reading between temperature readings) never
// evaluates its leaf to the FALSE that resolves the alarm / cancels the hold. When Complete is
// false the caller must fall back to feeding every event — the "raw-CEL author owns totality" trap
// documented on rules.CompiledRule; scoping an incompletely-understood leaf could drop a real raise.
//
// The set is returned sorted and de-duplicated so a compiled rule's feed scope is deterministic.
func (p *Predicate) MetricRefs() (metrics []string, complete bool) {
	return metricRefs(p.ast)
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
