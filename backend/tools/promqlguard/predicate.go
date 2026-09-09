// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package promqlguard answers one question about an alerting rule that neither
// `promtool check rules` nor a unit test over synthetic series reliably answers:
//
//	CAN THIS EXPRESSION EVER RETURN NOTHING?
//
// Prometheus fires an alert on the PRESENCE of a sample, not on its value, so an
// expression that always yields at least one sample is an alert that is always
// firing. That is worse than a missing alert: it trains whoever receives the page
// to ignore the channel it arrives on, and the alerts that matter arrive there too.
//
// 🔴 THE DEFECT PARSES CLEANLY, WHICH IS WHY THE PARSER IS THE INSTRUMENT. The shape
// this exists for is an author's comparison binding somewhere other than where they
// meant it to. `>` binds tighter than `or`, so
//
//	sum(rate(x[5m])) or vector(0) > 0
//
// reads as "the rate, defaulting to zero, above zero" and is not that: it parses as
// `sum(rate(x[5m])) or (vector(0) > 0)`, the right operand is empty, and what remains
// is a bare `sum(rate(x[5m]))` with no comparison left in it at all. It is valid
// PromQL meaning exactly what it says, so promtool accepts it, every series in it is
// spelled correctly, and it fired permanently on every instance at severity critical
// until someone read the rendered rule by hand.
//
// So the question here is about the SHAPE OF THE TREE, and asking it of the surface
// text is answering a parsing question with pattern matching. A regex version of this
// check was written and removed before merge because trivial respellings of the very
// defect it was written for walked past it — `or (vector(0)) > 0`, `or vector (0) > 0`,
// a `> bool 0`, a folded `expr: >` block whose YAML marker satisfied the comparison
// pattern, and a PromQL `#` comment — while legitimate alerts were rejected. Every one
// of those is the same tree to a parser.
//
// 🔴 THE COUNTERWEIGHTS MATTER AS MUCH AS THE KILLS. `(… or vector(0)) > 0` is not a
// near-miss of the defect, it is the CORRECT form of it and it must pass: without the
// `or vector(0)`, an alert summing several services goes ABSENT rather than false the
// moment one of them stops being scraped, which is the failure this kind of alert
// exists to catch. A gate that discouraged the idiom would cause what it prevents.
// `absent()`, `unless`, `and on()` and a deliberate dead-man's `vector(1)` must pass
// for the same reason — a guard that flags the whole corpus is a guard nobody keeps.
package promqlguard

import (
	"fmt"

	"github.com/prometheus/prometheus/promql/parser"
)

// parserOptions is deliberately the zero value: experimental syntax is off, which is
// how the Prometheus servers this platform ships rules for are configured. A rule
// using syntax the server will not parse should fail here rather than pass.
var parserOptions = parser.Options{}

// Predicate reports whether e has any state in which it returns NO samples, and when
// it does not, why not.
//
// The property is defined recursively over the operators that can and cannot remove
// series, which is the only reading of "can this be false?" that survives contact with
// real PromQL:
//
//   - a comparison WITHOUT `bool` filters, so it is a predicate;
//   - `and` and `unless` intersect and subtract, so their result can be empty;
//   - `or` is the one binary operator that can only ADD series. It is a predicate
//     exactly when BOTH of its operands are — which is what separates the legitimate
//     `max(x) == 0 or absent(x)` from the defect `sum(rate(x)) or vector(0) > 0`;
//   - an aggregation over an empty vector is empty in Prometheus (unlike SQL, where
//     `count` over no rows is 0), so `sum(...)` passes emptiness through;
//   - `absent()` and `absent_over_time()` are predicates by construction: they return
//     a sample only when their argument returns none;
//   - arithmetic computes a value and removes nothing, so `a / b`, `a - b` and
//     `a + b` are non-empty whenever their operands are.
//
// The returned string is empty when the first result is true.
func Predicate(e parser.Expr) (bool, string) {
	switch n := e.(type) {
	case *parser.ParenExpr:
		return Predicate(n.Expr)

	case *parser.StepInvariantExpr:
		return Predicate(n.Expr)

	case *parser.AggregateExpr:
		ok, why := Predicate(n.Expr)
		if ok {
			return true, ""
		}
		return false, fmt.Sprintf("`%s(...)` is empty exactly when what it aggregates is empty, and %s", n.Op, why)

	case *parser.BinaryExpr:
		switch {
		case n.Op.IsComparisonOperator() && n.ReturnBool:
			return false, fmt.Sprintf(
				"the top-level comparison carries the `bool` modifier, so `%s bool` returns a sample "+
					"for every series on the left whether the comparison holds or not — 1 where it "+
					"holds and 0 where it does not. An alert fires on the PRESENCE of a sample, not "+
					"on its value, so this fires whenever the left-hand side has data. Drop `bool`",
				n.Op)

		case n.Op.IsComparisonOperator():
			return true, ""

		case n.Op == parser.LAND, n.Op == parser.LUNLESS:
			// Both remove series from the left side, so the result can be empty
			// whatever the operands are.
			return true, ""

		case n.Op == parser.LOR:
			// 🔴 THE OPERATOR THIS GUARD EXISTS FOR. `or` is the only binary operator
			// that cannot make its result emptier, so it is a predicate only if
			// NEITHER side can stand on its own.
			if ok, why := Predicate(n.LHS); !ok {
				return false, fmt.Sprintf("the left-hand side of the top-level `or` is not a predicate: %s", why)
			}
			if ok, why := Predicate(n.RHS); !ok {
				return false, fmt.Sprintf("the right-hand side of the top-level `or` is not a predicate: %s", why)
			}
			return true, ""

		default:
			return false, fmt.Sprintf(
				"the top-level operator is `%s`, which computes a value rather than removing "+
					"series, so the result carries a sample whenever its operands do. If a "+
					"comparison was meant to apply to the whole expression, parenthesise it: "+
					"`(a %s b) > threshold`",
				n.Op, n.Op)
		}

	case *parser.Call:
		switch n.Func.Name {
		case "absent", "absent_over_time":
			// These are predicates by construction, in the other direction: a sample
			// only when the argument has none.
			return true, ""
		case "vector":
			return false, "`vector()` always returns exactly one sample, so nothing here can be empty"
		}
		return false, fmt.Sprintf(
			"`%s(...)` returns a value rather than filtering, so it carries a sample whenever "+
				"its argument does and nothing compares that value to anything",
			n.Func.Name)

	case *parser.NumberLiteral:
		return false, "a bare number always yields a sample"

	case *parser.VectorSelector:
		return false, "nothing is compared to anything, so this fires whenever the series exists"
	}

	return false, fmt.Sprintf("nothing in this expression can make it empty (%T at the top level)", e)
}

// DeadMansSwitch reports whether e is a bare `vector(<number>)` and nothing else.
//
// That expression is always-firing BY DESIGN and by convention — it is the Watchdog /
// DeadMansSwitch rule, whose whole job is to be the thing an operator notices the
// absence of when the alerting path itself has stopped. It is exempt as a WHOLE
// EXPRESSION rather than as an operand, which is the distinction that keeps the
// exemption from covering the defect: the `vector(0)` in `sum(rate(x)) or vector(0) > 0`
// is an operand, and this returns false for that expression.
//
// ⚠️ It is an exemption an author can reach for deliberately, and it is not policed
// further. `expr: vector(1)` on a rule that is not a dead-man's switch evades this
// guard. That is accepted: this instrument catches a comparison that binds where its
// author did not intend, not an alert someone chose to make unconditional.
func DeadMansSwitch(e parser.Expr) bool {
	for {
		p, ok := e.(*parser.ParenExpr)
		if !ok {
			break
		}
		e = p.Expr
	}
	call, ok := e.(*parser.Call)
	if !ok || call.Func == nil || call.Func.Name != "vector" || len(call.Args) != 1 {
		return false
	}
	_, isNumber := call.Args[0].(*parser.NumberLiteral)
	return isNumber
}

// CheckExpr parses expr and reports why it can never be empty, or "" when it can.
//
// A parse error is returned as an error rather than as a finding: this instrument
// cannot answer the question about an expression it cannot read, and "cannot answer"
// must never be spelled the same way as "nothing wrong". The caller exits 2 on it.
func CheckExpr(expr string) (string, error) {
	e, err := parser.NewParser(parserOptions).ParseExpr(expr)
	if err != nil {
		return "", fmt.Errorf("this expression does not parse as PromQL, so nothing can be concluded about it: %w", err)
	}
	if DeadMansSwitch(e) {
		return "", nil
	}
	ok, why := Predicate(e)
	if ok {
		return "", nil
	}
	return why, nil
}
