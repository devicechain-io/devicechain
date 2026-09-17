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

// 🔴 THE WIRING, AND THIS FILE IS ONLY THE WIRING. What the refusal DOES is tested in the
// bootstrap package against RefuseUnfinishedDestroy's own API; what is tested here is that
// the bootstrap command calls it, and calls it early enough to matter. A refusal that is
// correct and connected to nothing reads exactly like a refusal that is wired up — the
// same class as a credential minted on every run and placed in no Secret.
//
// It is a SOURCE assertion because the RunE creates clusters and talks to a live API, so
// no unit test in this module can execute it. That is the same case
// TestTheBootstrapCommandStillCarriesBothHalvesOfTheRecordRollback answers the same way.

// callPositions returns where each named call appears in the bootstrap command's source.
// A selector call (pkg.Name or value.Name) is matched on its final identifier; token.NoPos
// means "never called", which is the mutant these tests exist for.
func callPositions(t *testing.T, want map[string]bool) (map[string]token.Pos, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]token.Pos{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// The FIRST occurrence wins: what these tests are about is the earliest point the
		// call is made, and a later duplicate must not be able to satisfy an ordering
		// assertion the first one fails.
		if want[sel.Sel.Name] {
			if _, seen := found[sel.Sel.Name]; !seen {
				found[sel.Sel.Name] = call.Pos()
			}
		}
		return true
	})
	return found, fset
}

// 🔴 A HALF-DESTROYED INSTANCE IS REFUSED BEFORE THE RUN TOUCHES ANYTHING — the cluster it
// would create, and the local record it would replace.
//
// 🔑 AND THE RECORD IS THE REASON THE ORDER IS ASSERTED RATHER THAN THE UNWIND. The record
// of a marked instance is not a phantom: it is the only thing that names the cluster
// `dcctl destroy` has to finish the teardown in, and it was written by the bootstrap that
// built the instance. Refusing before WriteInstanceRecord leaves it untouched, which is
// what the destroy needs. Refusing AFTER it would replace it with this run's binding —
// which may name a different cluster — and the rollback would then be load-bearing. So
// this refusal is deliberately NOT in unwindLocalRecordWhenNothingWasWritten's error list:
// nothing to unwind, and a build that added it there while moving the check later would
// DELETE the record of a half-destroyed instance, which is the one thing destroy cannot
// do without.
func TestTheBootstrapCommandRefusesAHalfDestroyedInstanceBeforeItTouchesAnything(t *testing.T) {
	found, fset := callPositions(t, map[string]bool{
		"RefuseUnfinishedDestroy": true,
		"ValidateInstanceName":    true,
		"EnsureCluster":           true,
		"WriteInstanceRecord":     true,
	})

	refuse, ok := found["RefuseUnfinishedDestroy"]
	if !ok {
		t.Fatal("the bootstrap command no longer calls bootstrap.RefuseUnfinishedDestroy, so it " +
			"builds on top of an instance whose teardown did not finish — the cluster holds some " +
			"of the old one and no longer holds the rest, and the new run is blamed for it")
	}
	for _, later := range []struct {
		what string
		why  string
	}{
		{"EnsureCluster", "a refused run would have created or adopted a cluster first"},
		{"WriteInstanceRecord", "a refused run would have overwritten the record that names " +
			"the cluster `dcctl destroy` must finish the teardown in"},
	} {
		pos, ok := found[later.what]
		if !ok {
			t.Fatalf("bootstrap.go no longer calls %s, so this ordering assertion checks nothing", later.what)
		}
		if refuse > pos {
			t.Errorf("the teardown refusal is made at %s, after %s at %s — %s",
				fset.Position(refuse), later.what, fset.Position(pos), later.why)
		}
	}

	// 🔴 THE CONTROL FOR THE ASSERTIONS ABOVE. Both compare positions, and a comparison
	// against a call that is not there passes vacuously — so the ordering is also checked
	// against one whose place in this function is already settled and tested.
	if name, ok := found["ValidateInstanceName"]; ok && refuse < name {
		t.Errorf("the teardown refusal at %s runs before the name is validated at %s, so it "+
			"resolves a path from a name nothing has checked",
			fset.Position(refuse), fset.Position(name))
	}

	assertUnconditionalInRunE(t, "RefuseUnfinishedDestroy")
}

// assertUnconditionalInRunE checks that the named call is made from the Init of an `if` at
// the TOP LEVEL of the bootstrap command's RunE, and that neither that `if`'s condition nor
// its body mentions the dry-run flag.
//
// 🔴 THIS IS THE --dry-run ASSERTION, AND POSITION ALONE DOES NOT MAKE IT. A call moved
// inside `if !opts.DryRun { … }` keeps every ordering above intact and silently stops
// refusing a rehearsal — which is the run an operator uses to find out whether their
// arguments are usable, so it is the one that must answer soonest.
//
// 🔴 NESTING DEPTH ALONE DOES NOT MAKE IT EITHER, AND THAT IS WHY THE CONDITION IS READ
// TOO. `if err := X(); err != nil && !opts.DryRun { return err }` is at the top level,
// with the call in the Init, and skips the refusal on exactly the run this exists for. The
// depth check cannot see it, because what it tests is where the call sits and what changed
// is what is done with its RESULT.
//
// 🔑 WHAT IT DOES NOT CHECK, SO THAT NOBODY READS MORE INTO IT. It is a source assertion
// over one shape, not a proof that a rehearsal refuses: a dry-run branch anywhere ELSE in
// the RunE that returns before this statement is reached would pass it. The ordering
// assertions above are what bound where "before" is, and no unit test in this module can
// execute the RunE to settle it directly. A legitimate future mention of the flag in this
// statement therefore fails here on purpose — the shape is what is pinned.
func assertUnconditionalInRunE(t *testing.T, call string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var runE *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "RunE" {
			if lit, ok := kv.Value.(*ast.FuncLit); ok && runE == nil {
				runE = lit
			}
		}
		return true
	})
	if runE == nil {
		t.Fatal("bootstrap.go has no RunE function literal, so this check examined nothing")
	}

	names := func(n ast.Node) (found bool) {
		ast.Inspect(n, func(inner ast.Node) bool {
			c, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == call {
				found = true
			}
			return true
		})
		return found
	}

	// mentionsDryRun reports whether any identifier under n is a dry-run flag, by name.
	// Matched on the identifier rather than on `opts.DryRun` specifically, because the
	// command reads the flag under two spellings already (opts.DryRun and the
	// bootstrapDryRun package var) and a third would otherwise walk straight past.
	mentionsDryRun := func(n ast.Node) (found bool) {
		if n == nil {
			return false
		}
		ast.Inspect(n, func(inner ast.Node) bool {
			if id, ok := inner.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "dryrun") {
				found = true
			}
			return true
		})
		return found
	}

	for _, stmt := range runE.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		// The `if err := X(...); err != nil` shape: the call is in the Init, which makes
		// it unconditional — the condition is about its RESULT, not about reaching it.
		if ifStmt.Init == nil || !names(ifStmt.Init) {
			continue
		}
		// …but only while the condition really is about the result alone. A dry-run term
		// in it, or a dry-run branch in the body, turns the same statement back into one
		// a rehearsal walks past.
		if mentionsDryRun(ifStmt.Cond) || mentionsDryRun(ifStmt.Body) {
			t.Fatalf("the statement calling %s in the bootstrap RunE reads the dry-run flag at %s, "+
				"so a rehearsal does not get the refusal — and a rehearsal is the run an operator "+
				"uses to find out whether their arguments are usable", call, fset.Position(ifStmt.Pos()))
		}
		return
	}
	t.Fatalf("%s is not called unconditionally from the top of the bootstrap RunE, so some "+
		"runs — a --dry-run above all — skip the refusal entirely", call)
}
