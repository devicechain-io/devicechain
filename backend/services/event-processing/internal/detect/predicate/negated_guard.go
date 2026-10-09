// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"sort"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// NegatedAttributeGuards returns, sorted and de-duplicated, the device attributes the leaf tests
// for ABSENCE as a top-level conjunct: `!("k" in attr) && …`, `!has(attr.k) && …`, or the same test
// spelled `("k" in attr) == false`. The leaf is then true for every device that lacks the
// attribute and passes the rest of the test.
//
// It is the advisory companion to TrueWithoutAttributes, and is deliberately NOT a refusal: the
// fallback idiom `!("k" in attr) && m["t"] > 80.0` is what an author writes on purpose (see
// ErrTrueWithoutAttributes), but it reads as "only the devices configured without k" to an author
// who did not weigh the four ways a device has no entry in attr, so authoring surfaces warn.
//
// It is syntactic where TrueWithoutAttributes is semantic, because the question differs: that one
// asks whether the leaf is true for EVERY event, this one whether an absence test is conjoined, and
// partial evaluation cannot tell `!("k" in attr) && x` (unknown) from `("k" in attr) && x` (false)
// only by the keys involved. Not detected: a test reached through a `cel.bind` compute (the
// conjunct is then a bound variable, not the presence test) and one under `?:` or a negation. An
// absence test under `||` is the separate, worse DisjunctiveAttributeGuards.
func (p *Predicate) NegatedAttributeGuards() []string { return p.negatedAttrGuards }

// DisjunctiveAttributeGuards returns, sorted and de-duplicated, the attributes whose absence test is
// a DISJUNCT (`"t" in m && (!("k" in attr) || m["t"] > attr["k"])`). That is the worse shape: the
// leaf is true for every device lacking the attribute whatever its reading says, so the rule has
// no threshold at all for those devices. It is what an author writes after the compiler refuses a
// bare `!k || ...` and they add a metric guard in front. An attribute already reported by
// NegatedAttributeGuards is not repeated here.
func (p *Predicate) DisjunctiveAttributeGuards() []string { return p.disjunctiveAttrGuards }

// attributeAbsenceTests walks the &&/|| structure at the top of the checked AST and returns the
// attributes tested for absence as a conjunct and as a disjunct. It does not look under a
// negation, a ternary or any other call.
func attributeAbsenceTests(a *celast.AST) (conj, disj []string) {
	if a == nil {
		return nil, nil
	}
	conjSet, disjSet := map[string]struct{}{}, map[string]struct{}{}
	record := func(k string, inDisj bool) {
		if inDisj {
			disjSet[k] = struct{}{}
		} else {
			conjSet[k] = struct{}{}
		}
	}
	var walk func(e celast.Expr, inDisj bool)
	walk = func(e celast.Expr, inDisj bool) {
		if e.Kind() != celast.CallKind {
			return
		}
		c := e.AsCall()
		switch c.FunctionName() {
		case operators.LogicalAnd:
			for _, arg := range c.Args() {
				walk(arg, inDisj)
			}
		case operators.LogicalOr:
			for _, arg := range c.Args() {
				walk(arg, true)
			}
		case operators.LogicalNot:
			if len(c.Args()) == 1 {
				if k, ok := attrPresenceKey(c.Args()[0]); ok {
					record(k, inDisj)
				}
			}
		case operators.Equals:
			if len(c.Args()) == 2 {
				l, r := c.Args()[0], c.Args()[1]
				if isFalseLiteral(r) {
					l, r = r, l
				}
				if isFalseLiteral(l) {
					if k, ok := attrPresenceKey(r); ok {
						record(k, inDisj)
					}
				}
			}
		}
	}
	walk(a.Expr(), false)
	for k := range disjSet {
		if _, both := conjSet[k]; both {
			delete(disjSet, k)
		}
	}
	return sortedKeys(conjSet), sortedKeys(disjSet)
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// attrPresenceKey recognises the two spellings of a presence test on `attr` with a constant key —
// `"k" in attr` and `has(attr.k)` — and returns the key.
func attrPresenceKey(e celast.Expr) (string, bool) {
	switch e.Kind() {
	case celast.CallKind:
		c := e.AsCall()
		if c.FunctionName() != operators.In || len(c.Args()) != 2 {
			return "", false
		}
		key, m := c.Args()[0], c.Args()[1]
		if m.Kind() != celast.IdentKind || m.AsIdent() != VarAttr || key.Kind() != celast.LiteralKind {
			return "", false
		}
		s, ok := key.AsLiteral().(types.String)
		return string(s), ok
	case celast.SelectKind:
		s := e.AsSelect()
		if !s.IsTestOnly() {
			return "", false
		}
		op := s.Operand()
		if op.Kind() != celast.IdentKind || op.AsIdent() != VarAttr {
			return "", false
		}
		return s.FieldName(), true
	}
	return "", false
}

func isFalseLiteral(e celast.Expr) bool {
	return e.Kind() == celast.LiteralKind && e.AsLiteral() == types.False
}
