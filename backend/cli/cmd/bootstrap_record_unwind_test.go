// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
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
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "instances", instance)); err != nil {
		t.Fatalf("the fixture never wrote the record this test is about: %v", err)
	}
	return home, prior
}

func recordDirExists(t *testing.T, home, instance string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(home, ".devicechain", "instances", instance))
	return err == nil
}

func TestTheCommandLayerClearsTheRecordAfterAHostRefusal(t *testing.T) {
	home, prior := refusedHome(t, "bravo")

	// Wrapped the way Pipeline.Run wraps every step error — an unwrapped fixture would
	// pass while the real path never matched.
	refusal := fmt.Errorf("step %q: %w", "Check what other instances hold", &bootstrap.ErrHostTaken{
		Instance: "bravo", Host: "localhost", Holder: "alpha",
	})
	unwindLocalRecordOnHostTaken(bootstrap.Options{Instance: "bravo"}, prior, refusal)

	if recordDirExists(t, home, "bravo") {
		t.Fatal("`dcctl instances list` would still show an instance that was refused before " +
			"anything was installed, and no dcctl path can clear it")
	}
}

// The budget refusal fires at the same point as the host refusal — before anything is
// written — so it unwinds the record the same way.
func TestTheCommandLayerClearsTheRecordAfterABudgetRefusal(t *testing.T) {
	home, prior := refusedHome(t, "bravo")
	refusal := fmt.Errorf("step %q: %w", "Check what other instances hold", &bootstrap.ErrConnectionBudget{
		Err: errors.New("the shared relational store has no connection budget left for instance \"bravo\""),
	})
	unwindLocalRecordOnHostTaken(bootstrap.Options{Instance: "bravo"}, prior, refusal)
	if recordDirExists(t, home, "bravo") {
		t.Fatal("a bootstrap refused for its connection budget before anything was written left " +
			"a record `dcctl instances list` shows")
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
		// The same words, raised by the APPLY's admission rather than the early check:
		// by then the instance's namespace and declaration exist, and the record names them.
		{"a budget refusal that is not the typed early one", fmt.Errorf(
			"step \"Apply infrastructure\": %w", errors.New("no connection budget left on the shared store"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, prior := refusedHome(t, "bravo")
			unwindLocalRecordOnHostTaken(bootstrap.Options{Instance: "bravo"}, prior, tc.err)
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

	refusal := fmt.Errorf("step %q: %w", "Check what other instances hold",
		&bootstrap.ErrHostTaken{Instance: "bravo", Host: "localhost", Holder: "alpha"})
	unwindLocalRecordOnHostTaken(
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
	var capture, write, run, unwind, readInstall token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "unwindLocalRecordOnHostTaken" {
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
		case "ReadInstall":
			readInstall = call.Pos()
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
		{"unwindLocalRecordOnHostTaken", unwind},
	} {
		if c.pos == token.NoPos {
			t.Fatalf("the bootstrap command no longer calls %s, so a refused host "+
				"leaves a record `dcctl instances list` shows and no dcctl path can clear", c.what)
		}
	}

	// 🔴 THE IDENTITY READ IS IN THE SAME BOAT AND FOR THE SAME REASON. It happens in the
	// RunE nothing can execute, so deleting the whole block compiles, passes every test in
	// this module and changes no output an operator would notice — until something needs
	// to know which cluster a record belongs to and the answer was never written down.
	if readInstall == token.NoPos {
		t.Fatal("the bootstrap command no longer calls bootstrap.ReadInstall, so an instance is " +
			"built on a cluster nobody identified or checked was installed — or one whose install " +
			"record came from another cluster")
	}
	// 🔴 THE INSTALL IS CHECKED BEFORE ANYTHING IS WRITTEN. A refusal after the local
	// record is written leaves an instance `dcctl instances list` shows on a cluster that
	// holds nothing of it.
	if readInstall > write {
		t.Errorf("the install record is read at %s, after the instance record is written at %s "+
			"— an uninstalled cluster's refusal would leave a record behind",
			fset.Position(readInstall), fset.Position(write))
	}
	if capture > write {
		t.Errorf("the record is captured at %s, after it is replaced at %s — the rollback "+
			"would put back what this run itself wrote",
			fset.Position(capture), fset.Position(write))
	}
	// The identity has to be READ before the record that carries it is WRITTEN. Both calls
	// being present is not enough: with them the other way round the record is written
	// from a variable that is still empty, which produces exactly the pre-identity record
	// this is meant to replace — and nothing downstream can tell that apart from an
	// instance that genuinely predates the field.
	if readInstall > write {
		t.Errorf("the cluster is identified at %s, after the instance record is written at "+
			"%s — the record would carry no identity", fset.Position(readInstall), fset.Position(write))
	}
	if unwind < run {
		t.Errorf("the rollback is decided at %s, before the pipeline runs at %s, so it cannot "+
			"be keyed on the refusal", fset.Position(unwind), fset.Position(run))
	}
}

// 🔴 AN UNIDENTIFIABLE CLUSTER MUST STOP THE RUN, NOT WARN.
//
// The identity was a nicety while it only annotated a local record — an instance
// whose record lacked it was merely indistinguishable from one on a rebuilt cluster.
// After the root split it is the KEY THE SHARED PREREQUISITE STATE IS FILED UNDER, so
// carrying on has two possible meanings and both are worse than stopping: falling back
// to the context name files this cluster's state under a name the next cluster
// inherits, and skipping the prerequisite apply bootstraps an instance onto a cluster
// with no operator, no ingress and no database.
//
// 🔑 THIS IS A SOURCE-LEVEL ASSERTION BECAUSE THE BRANCH IS IN A RunE. It creates
// clusters and talks to a live API, so no unit test can execute it — the same case leg
// 3 met and answered the same way. A mutation round proved the gap: turning the refusal
// back into a warning SURVIVED, because the ordering guard above checks that the
// identity is READ and says nothing about what happens when it fails.
//
// So: in the switch over ReadInstall's error, every arm that does not return is either
// the success arm or reachable only on a dry run — an earlier `!opts.DryRun` arm returns.
func TestAnUnidentifiableClusterStopsTheBootstrap(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("reading bootstrap.go: %v", err)
	}
	file, err := parser.ParseFile(fset, "bootstrap.go", src, 0)
	if err != nil {
		t.Fatalf("parsing bootstrap.go: %v", err)
	}

	var checked bool
	ast.Inspect(file, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			// The shape `clusterUID, installRec, err := bootstrap.ReadInstall(...)`, and the
			// switch straight after it.
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 || i+1 >= len(block.List) {
				continue
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "ReadInstall" {
				continue
			}
			sw, ok := block.List[i+1].(*ast.SwitchStmt)
			if !ok {
				t.Fatalf("the ReadInstall call at %s is not followed by the switch that settles "+
					"its failures", fset.Position(call.Pos()))
			}
			checked = true

			var realRunRefused, identityRefused bool
			for _, c := range sw.Body.List {
				clause := c.(*ast.CaseClause)
				cond := "default"
				for _, e := range clause.List {
					cond = types.ExprString(e)
				}
				returns := len(clause.Body) > 0
				if returns {
					_, returns = clause.Body[len(clause.Body)-1].(*ast.ReturnStmt)
				}
				switch {
				case cond == "err == nil":
				case returns:
					if strings.Contains(cond, "!opts.DryRun") && strings.Contains(cond, `clusterUID == ""`) {
						identityRefused = true
					}
					if cond == "!opts.DryRun" {
						realRunRefused = true
					}
				case !realRunRefused:
					t.Errorf("the arm `%s` at %s warns instead of returning, and a real run "+
						"reaches it.\n"+
						"  A warning leaves clusterUID empty, and the shared prerequisite state then has\n"+
						"  no key: dcctl would either file it under the kube-context name (which the next\n"+
						"  cluster inherits) or skip the prerequisite apply entirely.",
						cond, fset.Position(clause.Pos()))
				}
			}
			if !identityRefused {
				t.Error("no arm refuses a real run on a cluster that could not be identified " +
					"(`case !opts.DryRun && clusterUID == \"\":` returning), so the refusal no longer " +
					"says it was the identity that failed")
			}
		}
		return true
	})

	if !checked {
		t.Fatal("bootstrap.go no longer has a `... := bootstrap.ReadInstall(...)` followed by a switch, " +
			"so nothing here can say what happens when a cluster cannot be identified")
	}
}
