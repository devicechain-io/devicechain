// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
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

// --keep-backups reaches the options both destroy forms build, and --all — which asks
// once and then destroys with --yes, so no per-instance prompt ever names an archive —
// says in its own prompt that backups go with the instances unless it is passed.
func TestKeepBackupsReachesTheDestroyOptionsAndTheBulkPrompt(t *testing.T) {
	orig := destroyKeepBackups
	t.Cleanup(func() { destroyKeepBackups = orig })
	for _, want := range []bool{true, false} {
		destroyKeepBackups = want
		if got := destroyOptionsFor("acme", "", false, true); got.KeepBackups != want {
			t.Errorf("--keep-backups=%v built options with KeepBackups=%v", want, got.KeepBackups)
		}
	}
	if f := destroyCmd.Flags().Lookup("keep-backups"); f == nil {
		t.Fatal("destroy has no --keep-backups flag")
	}
	for _, s := range []string{destroyAllPrompt(3, false), destroyAllBackupsNote(false)} {
		if !strings.Contains(s, "backups") || !strings.Contains(s, "--keep-backups") {
			t.Errorf("--all does not say its instances' backups are removed, or how to keep them: %q", s)
		}
	}
	if s := destroyAllPrompt(3, true); !strings.Contains(s, "kept") {
		t.Errorf("--all --keep-backups does not say the backups are kept: %q", s)
	}
}

// 🔴 THE DOCUMENTED RECOVERY NAMES --keep-backups. --restore-tsdb-from recovers by
// destroying the instance and rebuilding it, and a destroy without --keep-backups deletes
// the in-cluster archive the rebuild reads.
func TestTheRestoreFlagSaysToDestroyWithKeepBackups(t *testing.T) {
	f := bootstrapCmd.Flags().Lookup("restore-tsdb-from")
	if f == nil || !strings.Contains(f.Usage, "--keep-backups") {
		t.Fatalf("--restore-tsdb-from's help sends the operator to a destroy that deletes its archive: %v", f)
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
