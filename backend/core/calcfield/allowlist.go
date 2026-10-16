// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// allowedOperators are the CEL operators a formula may use, beyond the specially-checked
// index and `in`.
var allowedOperators = map[string]struct{}{
	operators.Add: {}, operators.Subtract: {}, operators.Multiply: {}, operators.Divide: {},
	operators.Modulo: {}, operators.Negate: {},
	operators.Equals: {}, operators.NotEquals: {}, operators.Less: {}, operators.LessEquals: {},
	operators.Greater: {}, operators.GreaterEquals: {},
	operators.LogicalAnd: {}, operators.LogicalOr: {}, operators.LogicalNot: {},
	operators.Conditional: {},
}

// conversions maps an allowed conversion function to the type it produces.
var conversions = map[string]Type{"double": Double, "int": Integer, "bool": Boolean}

// allowlist walks a TYPE-CHECKED expression and admits only the language this package
// documents. It is an allowlist, not a denylist: anything it does not recognise is refused,
// so a construct a future cel-go release adds to the standard environment is refused until
// this walker is taught it.
//
// It also derives the formula's inputs: every input key the expression names, as a variable,
// a read through m, or a presence test — and which of them it tests for presence anywhere.
type allowlist struct {
	env     *Env
	refs    map[int]struct{}
	guarded map[int]struct{}
	errs    []string
}

func (w *allowlist) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !slices.Contains(w.errs, msg) {
		w.errs = append(w.errs, msg)
	}
}

// check walks the expression and returns its input positions and, of those, the positions
// it never tests for presence (both ascending), or an error naming every refusal.
func (w *allowlist) check(root celast.Expr) (inputs, required []int, err error) {
	w.refs, w.guarded = map[int]struct{}{}, map[int]struct{}{}
	w.expr(root, 0)
	if len(w.errs) > 0 {
		return nil, nil, fmt.Errorf("%s", strings.Join(w.errs, "; "))
	}
	for i := range w.refs {
		inputs = append(inputs, i)
		if _, ok := w.guarded[i]; !ok {
			required = append(required, i)
		}
	}
	sort.Ints(inputs)
	sort.Ints(required)
	return inputs, required, nil
}

// expr checks one node. conv is the type of the conversion call directly enclosing it, or 0:
// a value read through m must sit directly inside the conversion matching its input's type.
func (w *allowlist) expr(e celast.Expr, conv Type) {
	switch e.Kind() {
	case celast.LiteralKind:
		switch v := e.AsLiteral().(type) {
		case types.Int, types.Double, types.Bool:
		case types.String:
			w.fail("string %q is not allowed: a string literal may only name an input, as m[%q] or %q in m", string(v), string(v), string(v))
		default:
			w.fail("literal %v of type %s is not allowed", v, v.Type().TypeName())
		}
	case celast.IdentKind:
		name := e.AsIdent()
		if name == VarM {
			w.fail("m may only be used as m[\"key\"], m.key, has(m.key) or \"key\" in m")
			return
		}
		i, ok := w.env.index[name]
		if !ok {
			// The checker only admits declared identifiers, so this is unreachable — but an
			// identifier this walker cannot place is refused, never waved through.
			w.fail("unknown identifier %q", name)
			return
		}
		w.refs[i] = struct{}{}
	case celast.SelectKind:
		sel := e.AsSelect()
		if !isM(sel.Operand()) {
			w.fail("field selection .%s is not allowed: only m.key reads an input", sel.FieldName())
			return
		}
		w.mAccess(sel.FieldName(), sel.IsTestOnly(), conv)
	case celast.CallKind:
		w.call(e.AsCall(), conv)
	case celast.ListKind:
		w.fail("lists are not allowed")
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

// call checks one function call.
func (w *allowlist) call(call celast.CallExpr, conv Type) {
	fn := call.FunctionName()
	args := call.Args()
	if call.IsMemberFunction() {
		w.fail("method call .%s() is not allowed", fn)
		return
	}
	switch fn {
	case operators.Index:
		if len(args) != 2 || !isM(args[0]) {
			w.fail("indexing is only allowed as m[\"key\"]")
			return
		}
		key, ok := stringLiteral(args[1])
		if !ok {
			w.fail("m must be indexed by a constant string key, as m[\"key\"]")
			return
		}
		w.mAccess(key, false, conv)
		return
	case operators.In:
		if len(args) != 2 || !isM(args[1]) {
			w.fail("`in` is only allowed as \"key\" in m")
			return
		}
		key, ok := stringLiteral(args[0])
		if !ok {
			w.fail("`in m` needs a constant string key, as \"key\" in m")
			return
		}
		w.mAccess(key, true, conv)
		return
	}
	if t, ok := conversions[fn]; ok {
		if len(args) != 1 {
			w.fail("%s() takes one argument", fn)
			return
		}
		w.expr(args[0], t)
		return
	}
	_, op := allowedOperators[fn]
	_, mathFn := mathFunctionNames[fn]
	if !op && !mathFn {
		w.fail("function %s is not allowed", displayName(fn))
		return
	}
	for _, a := range args {
		w.expr(a, 0)
	}
}

// mAccess checks a reference to input key through m: a presence test (has, in) or a read.
func (w *allowlist) mAccess(key string, presenceOnly bool, conv Type) {
	i, ok := w.env.index[key]
	if !ok {
		w.fail("%q is not a declared measured metric of this profile", key)
		return
	}
	w.refs[i] = struct{}{}
	if presenceOnly {
		w.guarded[i] = struct{}{}
		return
	}
	in := w.env.inputs[i]
	if conv != in.Type {
		w.fail("read the %s input %q as %s(m[%q])", in.Type, key, conversionFor(in.Type), key)
	}
}

// conversionFor is the conversion function that reads an input of type t through m.
func conversionFor(t Type) string {
	for fn, ct := range conversions {
		if ct == t {
			return fn
		}
	}
	return "?"
}

// displayName renders an operator's internal name the way an author would have typed it.
func displayName(fn string) string {
	if op, ok := operators.FindReverse(fn); ok && op != "" {
		return op
	}
	return fn + "()"
}

// isM reports whether e is the identifier m.
func isM(e celast.Expr) bool { return e.Kind() == celast.IdentKind && e.AsIdent() == VarM }

// stringLiteral returns the value of a string literal.
func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().(types.String)
	return string(s), ok
}
