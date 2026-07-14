// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"fmt"
	"strconv"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// LowerParams supplies the per-resolution bindings the lowered SQL fragment needs. The
// fragment is a bare WHERE predicate over the candidate member row (referenced by its
// real, unaliased table name so the rdb tenant-scope callback — which qualifies
// tenant_id with db.Statement.Table — stays unambiguous against the entity_attributes
// subquery). resolve.go composes it two ways over the same table: a paginated list and a
// single-entity membership test (§3.3). Keeping Lower pagination-agnostic is the
// forward-compat move for the deferred DETECT recompute (it reuses IsMember unchanged).
type LowerParams struct {
	// TenantId is the caller-tenant, emitted explicitly into every EA semi-join
	// (ea.tenant_id = ?) even though the rdb scope callback also constrains the outer
	// member row — the subquery is hand-written and the callback does not reach into it.
	TenantId string
	// MemberType is the entity family string stored in entity_attributes.entity_type
	// ("device"/"area"/…) — distinct from MemberTable, the SQL table of that family.
	MemberType string
	// MemberTable is the member family's SQL table (e.g. "devices"), used ONLY for the
	// ea.entity_id = <table>.id correlation. It is an internal, controlled identifier
	// (mapped from MemberType, never user input) — the one place a value is concatenated
	// rather than bound.
	MemberTable string
	// FacetScope pins which EntityAttribute scope defines facets. v1 = SHARED (§3.4).
	FacetScope string
	// NumericCast renders a value-column cast for the running dialect: "col::numeric" on
	// Postgres, "CAST(col AS REAL)" on sqlite (the oracle). The value_type IN ('LONG',
	// 'DOUBLE') gate guarantees the cast only ever sees writer-validated numeric text.
	NumericCast func(col string) string
}

// lowerCtx threads the params, the accumulating positional args, and the facet-leaf
// count through one recursive walk. The same walk validates (compile.go, discarding the
// fragment) and emits (Lower).
type lowerCtx struct {
	p      LowerParams
	args   []any
	leaves int
}

// Lower walks the selector's type-checked AST and returns the SQL WHERE fragment plus its
// positional args (gorm "?" placeholders, in fragment order). By the time resolve.go calls
// this the selector has already cleared the publish gate, so a NotLowerableError here is
// defensive. The returned fragment references p.MemberTable for the outer-row correlation.
func (s *Selector) Lower(p LowerParams) (string, []any, error) {
	lc := &lowerCtx{p: p}
	frag, err := lowerExpr(s.ast.NativeRep().Expr(), lc)
	if err != nil {
		if nl, ok := err.(*NotLowerableError); ok && nl.Source == "" {
			nl.Source = s.source
		}
		return "", nil, err
	}
	return frag, lc.args, nil
}

// lowerExpr lowers one boolean-position expression. Only a Call (a logical op, a
// comparison, or an `in`) is valid at a boolean position; anything else (a bare literal,
// a bare attr[k], an ident) is rejected — the top-level result must be a real predicate.
func lowerExpr(e celast.Expr, lc *lowerCtx) (string, error) {
	if e.Kind() != celast.CallKind {
		return "", notLowerable("expected a boolean facet comparison or &&/||/!")
	}
	return lowerCall(e.AsCall(), lc)
}

func lowerCall(call celast.CallExpr, lc *lowerCtx) (string, error) {
	fn := call.FunctionName()
	args := call.Args()
	switch fn {
	case operators.LogicalAnd, operators.LogicalOr:
		if len(args) != 2 {
			return "", notLowerable(fmt.Sprintf("%q expects two operands", fn))
		}
		l, err := lowerExpr(args[0], lc)
		if err != nil {
			return "", err
		}
		r, err := lowerExpr(args[1], lc)
		if err != nil {
			return "", err
		}
		sqlOp := "AND"
		if fn == operators.LogicalOr {
			sqlOp = "OR"
		}
		return "(" + l + " " + sqlOp + " " + r + ")", nil
	case operators.LogicalNot:
		if len(args) != 1 {
			return "", notLowerable("`!` expects one operand")
		}
		x, err := lowerExpr(args[0], lc)
		if err != nil {
			return "", err
		}
		return "(NOT " + x + ")", nil
	case operators.In, operators.OldIn:
		return lowerPresence(args, lc)
	case operators.Equals, operators.NotEquals:
		return lowerEquality(fn, args, lc)
	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
		return lowerNumericCompare(fn, args, lc)
	default:
		return "", notLowerable(fmt.Sprintf("unsupported operator or function %q", fn))
	}
}

// lowerPresence lowers `<key> in attr` to a presence-only semi-join (an absent facet is
// simply not a member). The left operand must be a string-literal key, the right the attr map.
func lowerPresence(args []celast.Expr, lc *lowerCtx) (string, error) {
	if len(args) != 2 {
		return "", notLowerable("`in` expects two operands")
	}
	if !isAttrIdent(args[1]) {
		return "", notLowerable("`in` right operand must be `attr`")
	}
	key, ok := stringLiteral(args[0])
	if !ok {
		return "", notLowerable("`in` left operand must be a string-literal facet key")
	}
	return lc.semiJoin(key, "", nil), nil
}

// lowerEquality lowers `attr[k] == lit` / `attr[k] != lit` (either operand order) to a
// present-and-equal / present-and-different semi-join.
func lowerEquality(fn string, args []celast.Expr, lc *lowerCtx) (string, error) {
	key, lit, _, ok := attrAndScalar(args)
	if !ok {
		return "", notLowerable("`==`/`!=` must compare attr[<string key>] to a scalar literal")
	}
	predSQL, predArgs, err := lc.valueEquality(lit, fn == operators.NotEquals)
	if err != nil {
		return "", err
	}
	return lc.semiJoin(key, predSQL, predArgs), nil
}

// lowerNumericCompare lowers `attr[k] <op> n` (or the commuted `n <op> attr[k]`, with the
// operator flipped) to a present-and-numerically-ordered semi-join. The literal must be numeric.
func lowerNumericCompare(fn string, args []celast.Expr, lc *lowerCtx) (string, error) {
	key, lit, flipped, ok := attrAndScalar(args)
	if !ok {
		return "", notLowerable("an ordered comparison must compare attr[<string key>] to a numeric literal")
	}
	sqlOp, err := numericSQLOp(fn, flipped)
	if err != nil {
		return "", err
	}
	predSQL, predArgs, err := lc.valueNumericCompare(sqlOp, lit)
	if err != nil {
		return "", err
	}
	return lc.semiJoin(key, predSQL, predArgs), nil
}

// semiJoin builds one EA(k, valPred) EXISTS correlated to the outer member row. The base
// args (tenant, member type, facet scope, key) are appended in fragment order, then the
// optional value predicate's own args, so the positional "?" order always matches.
func (lc *lowerCtx) semiJoin(key, predSQL string, predArgs []any) string {
	lc.leaves++
	var b strings.Builder
	b.WriteString("EXISTS (SELECT 1 FROM entity_attributes ea WHERE ea.tenant_id = ? AND ea.entity_type = ? AND ea.entity_id = ")
	b.WriteString(lc.p.MemberTable)
	b.WriteString(".id AND ea.scope = ? AND ea.deleted_at IS NULL AND ea.attr_key = ?")
	lc.args = append(lc.args, lc.p.TenantId, lc.p.MemberType, lc.p.FacetScope, key)
	if predSQL != "" {
		b.WriteString(" AND ")
		b.WriteString(predSQL)
		lc.args = append(lc.args, predArgs...)
	}
	b.WriteString(")")
	return b.String()
}

// valueEquality renders the value predicate for == (negate=false) / != (negate=true). The
// literal's CEL type both picks the predicate and pins value_type, so a leaf never matches
// across types (a JSON facet never satisfies a scalar leaf, and a bool never equals "true"
// the string). String/bool are plain text compares; numeric goes through numericCase so the
// cast is never evaluated on a non-numeric row (Postgres cast-safety — see numericCase).
func (lc *lowerCtx) valueEquality(lit celast.Expr, negate bool) (string, []any, error) {
	cmp := "="
	if negate {
		cmp = "<>"
	}
	switch v := lit.AsLiteral().Value().(type) {
	case string:
		return "ea.value_type = 'STRING' AND ea.value " + cmp + " ?", []any{v}, nil
	case bool:
		return "ea.value_type = 'BOOLEAN' AND ea.value " + cmp + " ?", []any{strconv.FormatBool(v)}, nil
	case int64:
		return lc.numericCase(cmp), []any{v}, nil
	case uint64:
		return lc.numericCase(cmp), []any{v}, nil
	case float64:
		return lc.numericCase(cmp), []any{v}, nil
	default:
		return "", nil, notLowerable(fmt.Sprintf("unsupported literal type %T in comparison", v))
	}
}

// valueNumericCompare renders the value predicate for an ordered comparison. Only a numeric
// literal is admissible (the gate rejects `<` on a string).
func (lc *lowerCtx) valueNumericCompare(sqlOp string, lit celast.Expr) (string, []any, error) {
	switch v := lit.AsLiteral().Value().(type) {
	case int64:
		return lc.numericCase(sqlOp), []any{v}, nil
	case uint64:
		return lc.numericCase(sqlOp), []any{v}, nil
	case float64:
		return lc.numericCase(sqlOp), []any{v}, nil
	default:
		return "", nil, notLowerable("an ordered comparison (`<` `<=` `>` `>=`) requires a numeric literal")
	}
}

// numericCase renders a numeric value comparison guarded so the cast is evaluated ONLY on a
// numeric-typed row. A bare `value_type IN ('LONG','DOUBLE') AND value::numeric <op> ?`
// would rely on AND short-circuiting to protect the cast — which Postgres does NOT guarantee
// (it may evaluate value::numeric on a STRING row holding non-numeric text for the same key
// and error at query time). A CASE guarantees the un-taken arm is not evaluated, on both
// Postgres and sqlite. The single `?` binds the numeric literal. (SetEntityAttribute also
// coerces any LONG/DOUBLE write to castable-or-NULL text, §3.4, so the taken arm is safe.)
func (lc *lowerCtx) numericCase(op string) string {
	return "CASE WHEN ea.value_type IN ('LONG','DOUBLE') THEN " +
		lc.p.NumericCast("ea.value") + " " + op + " ? ELSE FALSE END"
}

// numericSQLOp maps a CEL ordered operator to its SQL form, flipping it when the attr index
// is the right operand (`5 < attr[k]` ≡ `attr[k] > 5`).
func numericSQLOp(fn string, flipped bool) (string, error) {
	switch fn {
	case operators.Less:
		if flipped {
			return ">", nil
		}
		return "<", nil
	case operators.LessEquals:
		if flipped {
			return ">=", nil
		}
		return "<=", nil
	case operators.Greater:
		if flipped {
			return "<", nil
		}
		return ">", nil
	case operators.GreaterEquals:
		if flipped {
			return "<=", nil
		}
		return ">=", nil
	default:
		return "", notLowerable(fmt.Sprintf("unsupported ordered operator %q", fn))
	}
}

// attrAndScalar finds the `attr[<string key>]` operand and the scalar-literal operand of a
// binary comparison, in either order. flipped reports that the attr index was the RIGHT
// operand (so a caller can flip a non-commutative operator). ok is false unless exactly one
// side is a well-formed attr index and the other a scalar literal — which rejects
// attr[a]==attr[b], a bare attr[k], a non-literal key, and index-by-expression.
func attrAndScalar(args []celast.Expr) (key string, lit celast.Expr, flipped bool, ok bool) {
	if len(args) != 2 {
		return "", nil, false, false
	}
	if k, isAttr := attrIndexKey(args[0]); isAttr && isScalarLiteral(args[1]) {
		return k, args[1], false, true
	}
	if k, isAttr := attrIndexKey(args[1]); isAttr && isScalarLiteral(args[0]) {
		return k, args[0], true, true
	}
	return "", nil, false, false
}

// attrIndexKey returns the constant facet key of an `attr[<string literal>]` index and
// whether the expression is exactly that. Any other shape (a Select `attr.k`, a non-attr
// container, a non-literal or non-string key) yields ok=false.
func attrIndexKey(e celast.Expr) (string, bool) {
	if e.Kind() != celast.CallKind {
		return "", false
	}
	call := e.AsCall()
	switch call.FunctionName() {
	case operators.Index, operators.OptIndex:
	default:
		return "", false
	}
	a := call.Args()
	if len(a) != 2 || !isAttrIdent(a[0]) {
		return "", false
	}
	return stringLiteral(a[1])
}

// isAttrIdent reports whether e is the `attr` identifier.
func isAttrIdent(e celast.Expr) bool {
	return e.Kind() == celast.IdentKind && e.AsIdent() == VarAttr
}

// isScalarLiteral reports whether e is a string / int / uint / double / bool literal — the
// only literal kinds a facet leaf may compare against.
func isScalarLiteral(e celast.Expr) bool {
	if e.Kind() != celast.LiteralKind {
		return false
	}
	switch e.AsLiteral().Value().(type) {
	case string, int64, uint64, float64, bool:
		return true
	default:
		return false
	}
}

// stringLiteral returns the Go string value of a string-literal expression.
func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}

// notLowerable builds a NotLowerableError with the reason set; the Source is stamped by the
// caller (Compile / Lower) that holds the selector text.
func notLowerable(reason string) error {
	return &NotLowerableError{Reason: reason}
}
