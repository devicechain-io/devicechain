// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// 🔴 THE BOOTSTRAP SIDE OF THE FLIP IS ONE `if` IN A COMMAND, AND NOTHING ELSE CAN
// SEE IT.
//
// `dcctl bootstrap` stopped installing the operator and started refusing a cluster
// that has none. The refusal itself is tested in the bootstrap package against every
// answer a cluster can give; what no test there can reach is whether this command
// ASKS. Deleting the call compiles, passes every other test in the module, and
// restores exactly the behaviour the flip removed — a bootstrap writing an Instance
// against a CRD that may not be there, or may be a different release's.
//
// That is the shape this repository has been bitten by before: nothing exercises the
// wiring in a command's RunE, because reaching it needs a provider and a live
// cluster. So the call is held by the SOURCE, the way the record-rollback and
// cluster-identity checks in this package already are.
//
// 🔑 POSITION IS ASSERTED, NOT JUST PRESENCE. The guard has to run before the
// pipeline — steps 3 and 4 list the Instance declarations, and on a cluster with no
// CRD that read now refuses rather than answering "none". A guard placed after them
// would let the backstop fire first and report the wrong thing.
func TestTheBootstrapCommandAsksWhichOperatorTheClusterHas(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("reading bootstrap.go: %v", err)
	}
	file, err := parser.ParseFile(fset, "bootstrap.go", src, 0)
	if err != nil {
		t.Fatalf("parsing bootstrap.go: %v", err)
	}

	// Where each call appears in the source, in order. RunE is one linear block, so
	// source order is execution order for these three.
	var requireAt, readInstallAt, runPipelineAt int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "RequireOperator":
			requireAt = fset.Position(call.Pos()).Offset
		case "ReadInstall":
			readInstallAt = fset.Position(call.Pos()).Offset
		case "Run":
			// Pipeline.Run — the first step of which writes to the cluster.
			if runPipelineAt == 0 {
				runPipelineAt = fset.Position(call.Pos()).Offset
			}
		}
		return true
	})

	if requireAt == 0 {
		t.Fatal("dcctl bootstrap never calls bootstrap.RequireOperator.\n" +
			"Without it a bootstrap runs against whatever operator the cluster happens to " +
			"have — including none, and including a different release's — which is the " +
			"behaviour moving the operator into `dcctl install` exists to remove. The " +
			"refusal is tested in the bootstrap package; this is the only thing that can " +
			"see whether the command asks for it.")
	}
	if readInstallAt == 0 {
		t.Fatal("dcctl bootstrap never calls bootstrap.ReadInstall, so this test cannot " +
			"place the operator check relative to it")
	}
	if requireAt < readInstallAt {
		t.Error("the operator is checked before the install record is read. The record read " +
			"is what identifies the cluster and produces the install command the operator " +
			"refusal quotes, so this order would name a command built from nothing.")
	}
	if runPipelineAt != 0 && requireAt > runPipelineAt {
		t.Error("the operator is checked after the pipeline runs. Steps 3 and 4 list the " +
			"Instance declarations, and on a cluster with no CRD that read refuses — so the " +
			"backstop would fire first and report a missing definition instead of a missing " +
			"operator.")
	}
}
