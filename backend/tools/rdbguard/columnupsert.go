// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"go/ast"
	"go/token"
	"go/types"
)

// The column-array upsert (rdb.NewColumnUpsert) splices caller-written SQL fragments into a
// statement over a tenant table: the right-hand side of each SET assignment and the WHERE
// guard. rdb.Fragment is a named string type, so the compiler already refuses a string
// VARIABLE there; this rule closes the other doors (an explicit rdb.Fragment(x) conversion,
// and a Set list or Upsert value built somewhere the reader cannot see) by requiring every
// fragment, at the call that builds the table, to be a string literal, a `+` of them, or a
// package-level CONSTANT. The reviewer then reads every fragment in one place, and none of
// them was computed.
//
// It lives in the raw-SQL scan because that scan is the one that watches statements over
// tenant tables, and because the allow-list reason for ColumnTable.exec rests on this rule.

const columnUpsertFunc = "NewColumnUpsert"

// isColumnUpsertCall reports whether call is NewColumnUpsert / rdb.NewColumnUpsert, with or
// without explicit type arguments.
func isColumnUpsertCall(call *ast.CallExpr) bool {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == columnUpsertFunc
	case *ast.SelectorExpr:
		return f.Sel.Name == columnUpsertFunc
	}
	return false
}

// fragmentFindings reports every fragment of a NewColumnUpsert call that is not a literal or
// a constant, and a terms argument that is not itself a literal.
func (s *siteScanner) fragmentFindings(fset *token.FileSet, call *ast.CallExpr, p *pkgFacts) []Finding {
	var out []Finding
	flag := func(at ast.Expr, what string) {
		out = append(out, Finding{
			Pos: fset.Position(at.Pos()),
			Message: "column-array upsert " + what + " is not a string literal or a constant, so a " +
				"reviewer cannot read the SQL it splices into a statement over a tenant table",
			Source: types.ExprString(at),
		})
	}
	if len(call.Args) < 3 {
		flag(call, "terms")
		return out
	}
	terms, ok := unwrapLit(call.Args[2])
	if !ok {
		flag(call.Args[2], "terms (the Upsert argument)")
		return out
	}
	text := func(e ast.Expr, what string) {
		if !s.constText(e, p, 0) {
			flag(e, what)
		}
	}
	for _, el := range terms.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			flag(el, "terms (positional fields)")
			continue
		}
		switch types.ExprString(kv.Key) {
		case "Where":
			text(kv.Value, "WHERE fragment")
		case "Set":
			set, ok := unwrapLit(kv.Value)
			if !ok {
				flag(kv.Value, "SET list")
				continue
			}
			for _, a := range set.Elts {
				s.assignment(a, text, flag)
			}
		}
	}
	return out
}

// assignment checks one element of a SET list: rdb.SetTo(col, expr) or an Assignment literal.
func (s *siteScanner) assignment(e ast.Expr, text func(ast.Expr, string), flag func(ast.Expr, string)) {
	switch x := e.(type) {
	case *ast.CallExpr:
		name := ""
		switch f := x.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			name = f.Sel.Name
		}
		if name != "SetTo" || len(x.Args) != 2 {
			flag(e, "SET assignment")
			return
		}
		text(x.Args[0], "SET column")
		text(x.Args[1], "SET expression")
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				text(kv.Value, "SET assignment field")
			} else {
				text(el, "SET assignment field")
			}
		}
	default:
		flag(e, "SET assignment")
	}
}

// unwrapLit returns e as a composite literal, looking through & and parentheses.
func unwrapLit(e ast.Expr) (*ast.CompositeLit, bool) {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return nil, false
			}
			e = x.X
		case *ast.CompositeLit:
			return x, true
		default:
			return nil, false
		}
	}
}

// constText reports whether e is statically readable text: a string literal, a `+` of
// such, or a package-level constant (never a local, never a package var).
func (s *siteScanner) constText(e ast.Expr, p *pkgFacts, depth int) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Kind == token.STRING
	case *ast.ParenExpr:
		return s.constText(x.X, p, depth)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && s.constText(x.X, p, depth) && s.constText(x.Y, p, depth)
	case *ast.Ident:
		init, ok := p.strs[x.Name]
		return ok && depth < 8 && !s.locals[x.Name] && s.constText(init, p, depth+1)
	}
	return false
}
