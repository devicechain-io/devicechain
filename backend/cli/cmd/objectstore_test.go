// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/pflag"
)

func TestOnlyAnObjectStoreFlagCountsAsChoosingAStore(t *testing.T) {
	for _, name := range objectStoreFlags {
		if bootstrapCmd.Flags().Lookup(name) == nil {
			t.Errorf("objectStoreFlags names %s, which is not a bootstrap flag", name)
		}
		if !objectStoreFlagGiven(func(f string) bool { return f == name }) {
			t.Errorf("--%s does not count as choosing a store, so a re-run with it would be overruled by the declared one", name)
		}
	}
	if objectStoreFlagGiven(func(f string) bool { return f == "profile" || f == "host" }) {
		t.Error("an unrelated flag counts as choosing a store")
	}
	// Every flag that configures the store is on the list: a new --object-store-* flag that
	// is not here would silently be ignored by the carry-forward.
	bootstrapCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if strings.HasPrefix(f.Name, "object-store") {
			found := false
			for _, n := range objectStoreFlags {
				found = found || n == f.Name
			}
			if !found {
				t.Errorf("--%s is an object-store flag missing from objectStoreFlags", f.Name)
			}
		}
	})
}

func TestTheArgvCheckRefusesAnExplicitStoreThatCannotBeHonoured(t *testing.T) {
	if err := checkObjectStoreArgv(bootstrap.ObjectStoreFlags{}); err != nil {
		t.Errorf("no flag was refused: %v", err)
	}
	if err := checkObjectStoreArgv(bootstrap.ObjectStoreFlags{Mode: "filesystem", S3Bucket: "bucket"}); err == nil {
		t.Error("a bucket given with a filesystem store was accepted")
	}
	if err := checkObjectStoreArgv(bootstrap.ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Region: "us-east-1"}); err != nil {
		t.Errorf("a well-formed s3 store was refused: %v", err)
	}
}

// 🔴 THE FUNCTIONS ABOVE ARE USELESS IF THE COMMAND DOES NOT CALL THEM, AND THE COMMAND
// NEEDS A CLUSTER TO RUN. So the wiring is read from the source: the argv check runs before a
// cluster is created for the run, the resolved store is put on the State, and whether a flag
// was given is too.
func TestTheBootstrapCommandWiresTheObjectStore(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runE *ast.FuncLit
	ast.Inspect(f, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "RunE" {
				if fl, ok := kv.Value.(*ast.FuncLit); ok && runE == nil {
					runE = fl
				}
			}
		}
		return true
	})
	if runE == nil {
		t.Fatal("bootstrap.go no longer declares bootstrapCmd's RunE")
	}

	var argvCheck, ensureCluster token.Pos
	var stateStore, stateExplicit ast.Expr
	var resolveCalls int
	ast.Inspect(runE, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := n.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "checkObjectStoreArgv" {
					argvCheck = n.Pos()
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == "EnsureCluster" {
					ensureCluster = n.Pos()
				}
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "bootstrap" && fn.Sel.Name == "ResolveObjectStore" {
					resolveCalls++
				}
			}
		}
		// The State is built by bootstrapStateFor (split out of RunE so its field copies are
		// testable); the resolved store and the explicit flag are its 9th and 10th arguments.
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "bootstrapStateFor" && len(call.Args) >= 10 {
				stateStore, stateExplicit = call.Args[8], call.Args[9]
			}
		}
		return true
	})

	if argvCheck == token.NoPos {
		t.Error("bootstrapCmd never calls checkObjectStoreArgv: a bucket given with a filesystem store creates a cluster first")
	} else if ensureCluster != token.NoPos && argvCheck > ensureCluster {
		t.Error("the object-store argv check runs after the cluster is created")
	}
	if resolveCalls != 1 {
		t.Errorf("bootstrapCmd resolves the object store %d times with the cluster known, want 1", resolveCalls)
	}
	if id, ok := stateStore.(*ast.Ident); !ok || id.Name != "objectStore" {
		t.Errorf("the State is built with ObjectStore: %v, not the resolved store, so the instance is built without it", stateStore)
	}
	if call, ok := stateExplicit.(*ast.CallExpr); !ok || call.Fun.(*ast.Ident).Name != "objectStoreFlagGiven" {
		t.Errorf("the State is built with ObjectStoreExplicit: %v, so a flagless re-run cannot keep the declared store", stateExplicit)
	}
}
