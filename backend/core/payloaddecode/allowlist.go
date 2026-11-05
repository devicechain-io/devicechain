// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"slices"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/overloads"
	"github.com/google/cel-go/common/types"
)

// walker is the AST allowlist, run on a TYPE-CHECKED expression. It is a second fence behind
// the stdlib subset: it admits exactly the enumerated language below and refuses everything
// else, so a construct a later cel-go release adds to the standard library stays refused
// until this walker is taught it.
//
// Admitted:
//   - arithmetic and ordering on int, uint and double (uint in comparison and conversion);
//   - ==, != on int, uint, double, bool and string, where a string comparison, or one with a
//     dynamic operand, has a literal on one side (so its cost is bounded);
//   - `in` with a constant list of at most MaxInKeys literals;
//   - && || ! and ?:;
//   - field or index access on json, decoded, link and map-typed lets, with a CONSTANT string
//     key (or a chain of them): no computed keys;
//   - size on bytes; int, uint, double and bool conversions on scalars;
//   - the owned and math functions.
//
// Refused: list and map literals beyond the `in` case, comparison of bytes, string
// concatenation or any string function, member-call syntax, comprehensions.
type walker struct {
	ast  *celast.AST
	lets map[string]*types.Type // let name -> checked type, for root classification
	errs []string
	// usesJSON and usesDecoded record which input maps the expression reads.
	usesJSON, usesDecoded bool
	// dynSelects counts the field selections on an operand whose static type is not a map: the
	// runtime charges one unit for each, and cel-go's static estimate does not, so the
	// compiler adds them.
	dynSelects uint64
}

func (w *walker) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !slices.Contains(w.errs, msg) {
		w.errs = append(w.errs, msg)
	}
}

func (w *walker) typeOf(e celast.Expr) *types.Type { return w.ast.GetType(e.ID()) }

func isKind(t *types.Type, ks ...types.Kind) bool {
	return t != nil && slices.Contains(ks, t.Kind())
}

func (w *walker) numeric(e celast.Expr) bool {
	return isKind(w.typeOf(e), types.IntKind, types.UintKind, types.DoubleKind, types.DynKind)
}

func (w *walker) check(root celast.Expr) error {
	w.expr(root, false)
	if len(w.errs) > 0 {
		return fmt.Errorf("%s", strings.Join(w.errs, "; "))
	}
	return nil
}

// strOK says a string literal is permitted at this position.
func (w *walker) expr(e celast.Expr, strOK bool) {
	switch e.Kind() {
	case celast.LiteralKind:
		switch v := e.AsLiteral().(type) {
		case types.Int, types.Uint, types.Double, types.Bool:
		case types.String:
			if !strOK {
				w.fail("the string %q is only allowed in a comparison, an `in` list, a key, or as the text for hex.decode / base64.decode", string(v))
			}
		default:
			w.fail("a literal of type %s is not allowed", v.Type().TypeName())
		}
	case celast.IdentKind:
		w.ident(e.AsIdent())
	case celast.SelectKind:
		w.path(e)
	case celast.CallKind:
		w.call(e)
	case celast.ListKind:
		w.fail("lists are not allowed, except the constant list of an `in` test")
	case celast.MapKind:
		w.fail("map literals are not allowed")
	case celast.StructKind:
		w.fail("messages are not allowed")
	case celast.ComprehensionKind:
		w.fail("comprehensions are not allowed")
	default:
		w.fail("unsupported expression")
	}
}

// ident records which input maps an identifier reads.
func (w *walker) ident(name string) {
	switch name {
	case VarJSON:
		w.usesJSON = true
	case VarDecoded:
		w.usesDecoded = true
	}
}

// path checks a chain of constant-key accesses rooted at json, decoded, link or a map-typed
// (or dynamic) let.
func (w *walker) path(e celast.Expr) {
	switch e.Kind() {
	case celast.IdentKind:
		name := e.AsIdent()
		switch name {
		case VarJSON, VarDecoded, VarLink:
			w.ident(name)
			return
		}
		if t, ok := w.lets[name]; ok && isKind(t, types.MapKind, types.DynKind) {
			return
		}
		w.fail("%q cannot be indexed: only json, decoded, link and a let holding a map can", name)
	case celast.SelectKind:
		sel := e.AsSelect()
		if !sel.IsTestOnly() && !isKind(w.typeOf(sel.Operand()), types.MapKind, types.StructKind, types.TypeParamKind) {
			w.dynSelects++
		}
		w.path(sel.Operand())
	case celast.CallKind:
		c := e.AsCall()
		if c.FunctionName() != operators.Index || len(c.Args()) != 2 {
			w.fail("only constant-key access (json.key or json[\"key\"]) is allowed here")
			return
		}
		if _, ok := stringLiteral(c.Args()[1]); !ok {
			w.fail("a key must be a constant string, as json[\"key\"]: computed keys are not allowed")
		}
		w.path(c.Args()[0])
	default:
		w.fail("only constant-key access (json.key or json[\"key\"]) is allowed here")
	}
}

func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().(types.String)
	return string(s), ok
}

func isLiteral(e celast.Expr) bool { return e.Kind() == celast.LiteralKind }

func (w *walker) call(e celast.Expr) {
	c := e.AsCall()
	fn, args := c.FunctionName(), c.Args()
	if c.IsMemberFunction() {
		w.fail("method call .%s() is not allowed", fn)
		return
	}
	switch fn {
	case operators.Index:
		w.path(e)
		return
	case operators.In:
		w.in(args)
		return
	case operators.Equals, operators.NotEquals:
		if len(args) != 2 {
			return
		}
		for _, a := range args {
			if !isKind(w.typeOf(a), types.IntKind, types.UintKind, types.DoubleKind, types.BoolKind, types.StringKind, types.DynKind) {
				w.fail("%s compares int, uint, double, bool and string values only", displayName(fn))
			}
		}
		// Bound the comparison's cost: a string comparison, or one with a dynamic operand,
		// needs a literal on one side.
		needLit := false
		for _, a := range args {
			if isKind(w.typeOf(a), types.StringKind, types.DynKind) {
				needLit = true
			}
		}
		if needLit && !isLiteral(args[0]) && !isLiteral(args[1]) {
			w.fail("%s on a string or a dynamic value needs a literal on one side", displayName(fn))
		}
		w.expr(args[0], true)
		w.expr(args[1], true)
		return
	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals,
		operators.Add, operators.Subtract, operators.Multiply, operators.Divide, operators.Modulo, operators.Negate:
		for _, a := range args {
			if !w.numeric(a) {
				w.fail("%s works on int, uint and double values only", displayName(fn))
			}
			w.expr(a, false)
		}
		return
	case operators.LogicalAnd, operators.LogicalOr, operators.LogicalNot, operators.Conditional:
		for _, a := range args {
			w.expr(a, false)
		}
		return
	case overloads.Size:
		if len(args) != 1 || !isKind(w.typeOf(args[0]), types.BytesKind) {
			w.fail("size() works on bytes only")
		}
		for _, a := range args {
			w.expr(a, false)
		}
		return
	case overloads.TypeConvertInt, overloads.TypeConvertUint, overloads.TypeConvertDouble, overloads.TypeConvertBool:
		if len(args) != 1 {
			w.fail("%s() takes one argument", fn)
			return
		}
		if !isKind(w.typeOf(args[0]), types.IntKind, types.UintKind, types.DoubleKind, types.BoolKind, types.DynKind) {
			w.fail("%s() converts a number or a boolean only", fn)
		}
		w.expr(args[0], false)
		return
	}
	if _, ok := byName[fn]; ok {
		for i, a := range args {
			// The text a decode function reads may be a literal.
			w.expr(a, i == 0 && (fn == "hex.decode" || fn == "base64.decode"))
		}
		return
	}
	if _, ok := mathFunctionNames[fn]; ok {
		for _, a := range args {
			w.expr(a, false)
		}
		return
	}
	w.fail("function %s is not allowed", displayName(fn))
}

// in checks `x in [literals]`: a constant list of at most MaxInKeys literals.
func (w *walker) in(args []celast.Expr) {
	if len(args) != 2 || args[1].Kind() != celast.ListKind {
		w.fail("`in` is only allowed against a constant list of literals")
		return
	}
	elems := args[1].AsList().Elements()
	if len(elems) > MaxInKeys {
		w.fail("an `in` list has %d entries, above the maximum of %d", len(elems), MaxInKeys)
	}
	for _, el := range elems {
		if !isLiteral(el) {
			w.fail("an `in` list may hold literals only")
			continue
		}
		w.expr(el, true)
	}
	w.expr(args[0], false)
}

// displayName renders an operator's internal name the way an author would have typed it.
func displayName(fn string) string {
	if op, ok := operators.FindReverse(fn); ok && op != "" {
		return op
	}
	return fn + "()"
}
