// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// holdAssignments returns the names X for which main.go assigns X.Hold from newStoreHold(...)
// or storehold.New(...).
//
// 🔴 A processor test builds its own consumer, so a main.go that stopped giving a consumer its
// store hold would leave every processor test green while a database outage lettered and acked
// that consumer's messages at their last delivery again. The consumers are built inside the
// broker's construction callback, which only a whole service runs; this reads what main.go
// assigns.
func holdAssignments(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Hold" {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		call, ok2 := as.Rhs[0].(*ast.CallExpr)
		if !ok || !ok2 {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == "newStoreHold" {
				out[x.Name] = true
			}
		case *ast.SelectorExpr:
			if p, isIdent := f.X.(*ast.Ident); isIdent && p.Name == "storehold" && f.Sel.Name == "New" {
				out[x.Name] = true
			}
		}
		return true
	})
	return out
}

func TestEveryDatabaseConsumerIsBuiltWithAStoreHold(t *testing.T) {
	got := holdAssignments(t)
	for _, name := range []string{"NotificationProcessor"} {
		require.True(t, got[name], "main.go no longer gives %s its store hold", name)
	}
}
