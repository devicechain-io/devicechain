// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The overlay's functions are tested on their own; these hold their CALLERS. Upgrade and the
// bootstrap's singleton step need a cluster to drive, so a call dropped from either -- or moved
// past the first write -- would leave every function test green while the verb stopped
// checking, reporting or recording the overlay. So the calls are held by the source.

// overlayCalls returns, for the named function in file, the source offset of every call to
// each name, in order, and whether a call to record sits under `if valuesOverlayChanges(st)`.
func overlayCalls(t *testing.T, file, fn string) (calls map[string][]token.Pos, gated map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls, gated = map[string][]token.Pos{}, map[string]bool{}
	found := false
	for _, d := range f.Decls {
		decl, ok := d.(*ast.FuncDecl)
		if !ok || decl.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(decl, func(n ast.Node) bool {
			if ifs, ok := n.(*ast.IfStmt); ok {
				if c, ok := ifs.Cond.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "valuesOverlayChanges" {
						ast.Inspect(ifs.Body, func(m ast.Node) bool {
							if call, ok := m.(*ast.CallExpr); ok {
								if id, ok := call.Fun.(*ast.Ident); ok {
									gated[id.Name] = true
								}
							}
							return true
						})
					}
				}
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok {
					calls[id.Name] = append(calls[id.Name], call.Pos())
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s has no function %s, so its overlay wiring cannot be checked", file, fn)
	}
	return calls, gated
}

func TestUpgradeChecksReportsAndRecordsTheOverlayBeforeItMovesAnything(t *testing.T) {
	calls, gated := overlayCalls(t, "upgrade.go", "Upgrade")
	for _, want := range []string{"precheckValuesOverlay", "sayValuesOverlay", "recordValuesOverlay", "recordUpgradedVersion", "precheckUpgradeLogin"} {
		if len(calls[want]) == 0 {
			t.Errorf("Upgrade no longer calls %s", want)
		}
	}
	if t.Failed() {
		return
	}
	// The render check comes before the first write (the version record) and before the
	// connection-budget check that reads the same overlay.
	pre := calls["precheckValuesOverlay"][0]
	for _, later := range []string{"recordUpgradedVersion", "recordValuesOverlay", "precheckUpgradeLogin"} {
		if pre > calls[later][0] {
			t.Errorf("Upgrade checks the overlay after %s: a refusal would come after something moved", later)
		}
	}
	if !gated["recordValuesOverlay"] {
		t.Error("Upgrade does not record the overlay under `if valuesOverlayChanges(st)`: either every upgrade " +
			"rewrites the declaration, or none records the operator's change")
	}
}

func TestTheSingletonStepRendersTheOverlayBeforeAskingTheStore(t *testing.T) {
	calls, _ := overlayCalls(t, "singletons.go", "stepCheckClusterSingletons")
	pre := calls["precheckValuesOverlay"]
	store := calls["precheckSharedStore"]
	// One for the rehearsal, one for the real run.
	if len(pre) < 2 || len(store) == 0 {
		t.Fatalf("stepCheckClusterSingletons calls precheckValuesOverlay %d time(s) and precheckSharedStore %d: "+
			"the real run no longer renders the overlay before anything is written", len(pre), len(store))
	}
	if pre[len(pre)-1] > store[0] {
		t.Error("the real run renders the overlay after asking the store")
	}
}

func TestTheBootstrapDeclarationRecordsAndReportsTheOverlay(t *testing.T) {
	calls, gated := overlayCalls(t, "step_claim.go", "stepDeclareInstance")
	for _, want := range []string{"adoptRecordedValuesOverlay", "sayValuesOverlay"} {
		if len(calls[want]) == 0 {
			t.Errorf("stepDeclareInstance no longer calls %s", want)
		}
	}
	if !gated["recordValuesOverlayFor"] {
		t.Error("stepDeclareInstance does not record the overlay under `if valuesOverlayChanges(st)`")
	}
}

// The record is bounded where it is WRITTEN, not only where a file is read: several files,
// each under the cap, can merge into one over it.
func TestTheRecordIsBoundedWhenItIsWritten(t *testing.T) {
	big := map[string]interface{}{"metrics": map[string]interface{}{"alerts": strings.Repeat("x", maxValuesOverlayBytes)}}
	if _, err := encodeValuesOverlay(big); err == nil {
		t.Fatal("an overlay over the annotation cap was encoded")
	}
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	if err := recordValuesOverlay(t.Context(), dyn, "prod", big); err == nil {
		t.Error("an overlay over the annotation cap was recorded")
	}
	if _, has := readBack(t, dyn, "prod").Annotations[AnnotationValuesOverlay]; has {
		t.Error("a refused overlay left an annotation behind")
	}
}
