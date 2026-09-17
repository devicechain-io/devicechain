// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// --without-state reaches the options both destroy forms build.
func TestWithoutStateReachesTheDestroyOptions(t *testing.T) {
	orig := destroyWithoutState
	t.Cleanup(func() { destroyWithoutState = orig })
	for _, want := range []bool{true, false} {
		destroyWithoutState = want
		if got := destroyOptionsFor("acme", "", false, true); got.WithoutState != want {
			t.Errorf("--without-state=%v built options with WithoutState=%v", want, got.WithoutState)
		}
	}
	if f := destroyCmd.Flags().Lookup("without-state"); f == nil {
		t.Error("destroy has no --without-state flag")
	}
}

// 🔴 AND NO FORM BUILDS ITS OWN. --all once had a literal of its own, and a flag added to
// one literal is silently absent from the other: --without-state would reach a single
// destroy and not the bulk one. So destroyOptionsFor is the only place the type is built.
func TestEveryDestroyFormBuildsItsOptionsInOnePlace(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "destroy.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	literals := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if x, ok := n.(*ast.CompositeLit); ok {
			if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "DestroyOptions" {
				literals++
			}
		}
		return true
	})
	// The one literal is destroyOptionsFor's; a second anywhere — including the command's
	// RunE closure, which is not a declared function — is a form building its own.
	if literals != 1 {
		t.Errorf("destroy.go builds DestroyOptions in %d places, want 1 (destroyOptionsFor)", literals)
	}
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			ast.Inspect(fn, func(n ast.Node) bool {
				if x, ok := n.(*ast.CallExpr); ok {
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "destroyOptionsFor" {
						calls[fn.Name.Name]++
					}
				}
				return true
			})
		}
	}
	if calls["destroyEveryInstance"] == 0 {
		t.Error("--all (destroyEveryInstance) does not build its options through destroyOptionsFor")
	}
}
