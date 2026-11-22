// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

func TestAttributeOrphanSweepLifecycle(t *testing.T) {
	if attributeOrphanSweepInterval != 60*time.Second {
		t.Fatal("orphan sweep must run every 60 seconds")
	}
	calls := map[string]bool{}
	for _, path := range []string{"main.go", "attribute_orphan_sweep.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		local := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				calls[fn.Name] = true
				local[fn.Name] = true
			case *ast.SelectorExpr:
				if receiver, ok := fn.X.(*ast.Ident); ok {
					calls[receiver.Name+"."+fn.Sel.Name] = true
					local[receiver.Name+"."+fn.Sel.Name] = true
				}
			}
			return true
		})
		if path == "attribute_orphan_sweep.go" && !local["core.WithImmediateFirstPass"] {
			t.Fatal("orphan sweep must request an immediate first pass")
		}
	}
	for _, call := range []string{"initAttributeOrphanSweep", "AttributeOrphanSweep.Initialize", "AttributeOrphanSweep.Start", "AttributeOrphanSweep.Stop", "AttributeOrphanSweep.Terminate", "Api.SweepAllDeviceAttributeOrphans", "core.WithImmediateFirstPass"} {
		if !calls[call] {
			t.Errorf("orphan sweep lifecycle is missing %s", call)
		}
	}
}
