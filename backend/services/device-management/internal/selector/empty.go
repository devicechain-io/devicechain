// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// MatchesEmpty reports whether the selector matches an entity with no attributes.
// It mirrors Lower: each leaf is an EXISTS semi-join over no rows (false), including
// !=, whose negate flag changes the value comparison INSIDE EXISTS, and !, &&, ||
// fold those predicates exactly as SQL does. Leaf validation uses the lowering itself.
func (s *Selector) MatchesEmpty() (bool, error) {
	lc := &lowerCtx{p: LowerParams{NumericCast: func(col string) string { return col }}}
	match, err := matchesEmptyExpr(s.ast.NativeRep().Expr(), lc)
	if nl, ok := err.(*NotLowerableError); ok && nl.Source == "" {
		nl.Source = s.source
	}
	return match, err
}

func matchesEmptyExpr(e celast.Expr, lc *lowerCtx) (bool, error) {
	if e.Kind() == celast.CallKind {
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case operators.LogicalNot:
			if len(args) == 1 {
				x, err := matchesEmptyExpr(args[0], lc)
				return !x, err
			}
		case operators.LogicalAnd, operators.LogicalOr:
			if len(args) == 2 {
				l, err := matchesEmptyExpr(args[0], lc)
				if err != nil {
					return false, err
				}
				// Visit both operands even when the left decides the result: Lower
				// rejects unsupported nodes in either branch, without short-circuiting.
				r, err := matchesEmptyExpr(args[1], lc)
				if err != nil {
					return false, err
				}
				if call.FunctionName() == operators.LogicalAnd {
					return l && r, nil
				}
				return l || r, nil
			}
		}
	}
	// All accepted leaves use semiJoin's EXISTS, including presence, equality,
	// inequality and ordered comparisons. Reuse its validation and error messages
	// also for malformed logical calls and non-call nodes.
	_, err := lowerExpr(e, lc)
	return false, err
}
