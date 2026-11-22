// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestExecutorCommandSurface(t *testing.T) {
	for _, name := range []string{"enroll", "status", "unenroll"} {
		t.Run(name, func(t *testing.T) {
			c, rest, err := rootCmd.Find([]string{"executor", name})
			if err != nil || len(rest) != 0 || c.Name() != name {
				t.Fatalf("executor %s is missing: command=%s rest=%v error=%v", name, c.Name(), rest, err)
			}
			if !strings.Contains(c.Long, "Preview: the executor is installed and verified but executes no operations in this release") {
				t.Error("the command does not explain its Preview limitation")
			}
			if c.Flag("kube-context") == nil {
				t.Error("missing --kube-context")
			}
			flags := map[string][]string{"enroll": {"owner", "image", "escrow-dir", "dry-run"}, "unenroll": {"force"}}
			for _, flag := range flags[name] {
				if c.Flag(flag) == nil {
					t.Errorf("missing --%s", flag)
				}
			}
			if err := c.Args(c, []string{"unexpected"}); err == nil {
				t.Error("unexpected positional argument accepted")
			}
		})
	}
}

// A source assertion is needed here: reaching bootstrap's RunE normally requires
// a provider and a cluster. Hold both the call and its position before any writes.
func TestBootstrapCallsPendingOperationRefusal(t *testing.T) {
	src, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "bootstrap.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var operator, refusal, write token.Pos
	var skipsDryRun bool
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "RequireOperator":
					operator = call.Pos()
				case "CheckPendingOperation":
					refusal = call.Pos()
				case "WriteInstanceRecord":
					write = call.Pos()
				}
			}
		}
		if branch, ok := n.(*ast.IfStmt); ok {
			if neg, ok := branch.Cond.(*ast.UnaryExpr); ok && neg.Op == token.NOT {
				if sel, ok := neg.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "DryRun" {
					ast.Inspect(branch.Body, func(inner ast.Node) bool {
						if call, ok := inner.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CheckPendingOperation" {
								skipsDryRun = true
							}
						}
						return true
					})
				}
			}
		}
		return true
	})
	if operator == 0 || refusal == 0 || write == 0 || refusal < operator || refusal > write {
		t.Fatalf("bootstrap must call pending-operation refusal after RequireOperator and before WriteInstanceRecord: %v %v %v", operator, refusal, write)
	}
	if !skipsDryRun || !strings.Contains(string(src), "[dry-run] skipping the pending-operation") {
		t.Error("dry-run must skip the pending-operation lookup with a visible note")
	}
}
