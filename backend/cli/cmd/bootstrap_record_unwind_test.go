// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// 🔴 THIS IS THE WIRING TEST, AND IT IS THE HALF NOTHING ELSE COVERS. The rollback
// machinery is tested in the bootstrap package against its own API; what is tested here
// is that the command layer CALLS it, and calls it for the right error. A capture and a
// Restore that are both correct and connected to nothing is the recurring defect this
// pair exists to rule out.

// refusedHome puts a record on disk exactly as `dcctl bootstrap` does before the pipeline
// starts, and returns the capture taken beforehand.
func refusedHome(t *testing.T, instance string) (home string, prior bootstrap.PriorLocalState) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	prior = bootstrap.CapturePriorLocalState(instance)
	if err := bootstrap.WriteInstanceRecord(bootstrap.InstanceRecord{
		Instance: instance, Provider: "local", Cluster: "alpha-cluster",
		KubeContext: "kind-alpha-cluster",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", instance)); err != nil {
		t.Fatalf("the fixture never wrote the record this test is about: %v", err)
	}
	return home, prior
}

func recordDirExists(t *testing.T, home, instance string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(home, ".devicechain", instance))
	return err == nil
}

func TestTheCommandLayerClearsTheRecordAfterASecondInstanceRefusal(t *testing.T) {
	home, prior := refusedHome(t, "bravo")

	// Wrapped the way Pipeline.Run wraps every step error — an unwrapped fixture would
	// pass while the real path never matched.
	refusal := fmt.Errorf("step %q: %w", "Refuse a second instance", &bootstrap.ErrSecondInstance{
		Holds: []string{"alpha"}, Wanted: "bravo", Provider: "local",
	})
	unwindLocalRecordOnSecondInstance(bootstrap.Options{Instance: "bravo"}, prior, refusal)

	if recordDirExists(t, home, "bravo") {
		t.Fatal("`dcctl instances list` would still show an instance that was refused before " +
			"anything was installed, and no dcctl path can clear it")
	}
}

// 🔴 THE NEGATIVE CONTROL, AND IT IS WHY THIS IS KEYED ON THE ERROR RATHER THAN ON
// FAILURE. Every other way a bootstrap can fail may have left a cluster half-built, and
// the record is the only thing that can name it. Widening this to "any error" restores
// the orphan the record exists to prevent.
func TestEveryOtherFailureKeepsTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a step that failed for some other reason", errors.New("step \"Apply infrastructure\": boom")},
		{"the rebuild refusal, which is about the SAME instance", errors.New(
			"step \"Refuse a rebuild\": instance \"bravo\" is already running in this cluster")},
		{"a successful run", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, prior := refusedHome(t, "bravo")
			unwindLocalRecordOnSecondInstance(bootstrap.Options{Instance: "bravo"}, prior, tc.err)
			if !recordDirExists(t, home, "bravo") {
				t.Fatal("the record was cleared after a failure that may have left a cluster " +
					"behind, so nothing can name the cluster to destroy it")
			}
		})
	}
}

// A dry run writes no record, so there is nothing to undo — and undoing anyway would act
// on a starting point this run never changed.
func TestADryRunIsNotUnwound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prior := bootstrap.CapturePriorLocalState("bravo")
	// A live record for the same name, of the kind a dry run must never touch.
	if err := bootstrap.WriteInstanceRecord(bootstrap.InstanceRecord{
		Instance: "bravo", Provider: "local", Cluster: "bravo-cluster",
	}); err != nil {
		t.Fatal(err)
	}

	refusal := fmt.Errorf("step %q: %w", "Refuse a second instance",
		&bootstrap.ErrSecondInstance{Holds: []string{"alpha"}, Wanted: "bravo"})
	unwindLocalRecordOnSecondInstance(
		bootstrap.Options{Instance: "bravo", DryRun: true}, prior, refusal)

	if !recordDirExists(t, home, "bravo") {
		t.Fatal("a --dry-run removed a record it never wrote")
	}
}

// 🔴 THE TESTS ABOVE DRIVE THE HELPER, NOT THE COMMAND, AND A MUTANT PROVED IT MATTERS.
// Deleting the call from the bootstrap command's RunE left every one of them green: they
// build the arguments themselves and call the function directly, so a correct helper
// wired to nothing reads exactly like a correct helper wired up. That is the same class
// as a credential minted on every run and placed in no Secret.
//
// The RunE cannot be executed in a unit test — it creates clusters — so what is asserted
// is the SOURCE: that both halves are still there, and in the only order that works. The
// capture has to precede the write that replaces what it captured, and the unwind has to
// follow the run whose error it keys on.
func TestTheBootstrapCommandStillCarriesBothHalvesOfTheRecordRollback(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	// Where each of the four calls appears in the command's source. NoPos means "never
	// called", which is the mutant this exists for.
	var capture, write, run, unwind token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "unwindLocalRecordOnSecondInstance" {
			unwind = call.Pos()
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "CapturePriorLocalState":
			capture = call.Pos()
		case "WriteInstanceRecord":
			write = call.Pos()
		case "Run":
			// NewDefaultPipeline().Run(...), matched through its receiver rather than by
			// a name as common as "Run".
			if inner, ok := sel.X.(*ast.CallExpr); ok {
				if innerSel, ok := inner.Fun.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "NewDefaultPipeline" {
					run = call.Pos()
				}
			}
		}
		return true
	})

	for _, c := range []struct {
		what string
		pos  token.Pos
	}{
		{"bootstrap.CapturePriorLocalState", capture},
		{"bootstrap.WriteInstanceRecord", write},
		{"NewDefaultPipeline().Run", run},
		{"unwindLocalRecordOnSecondInstance", unwind},
	} {
		if c.pos == token.NoPos {
			t.Fatalf("the bootstrap command no longer calls %s, so a refused second instance "+
				"leaves a record `dcctl instances list` shows and no dcctl path can clear", c.what)
		}
	}
	if capture > write {
		t.Errorf("the record is captured at %s, after it is replaced at %s — the rollback "+
			"would put back what this run itself wrote",
			fset.Position(capture), fset.Position(write))
	}
	if unwind < run {
		t.Errorf("the rollback is decided at %s, before the pipeline runs at %s, so it cannot "+
			"be keyed on the refusal", fset.Position(unwind), fset.Position(run))
	}
}
