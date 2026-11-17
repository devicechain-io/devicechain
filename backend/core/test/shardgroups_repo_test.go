// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

// checkShardGroupEnable accepts a source buffer for fixtures, or nil to read path.
// Resolve the import so comments and unrelated Enable calls cannot satisfy the check.
func checkShardGroupEnable(path string, src any) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	imports := importNames(f)
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Enable" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && imports[id.Name] == "github.com/devicechain-io/dc-microservice/shardgroups" {
			found = true
		}
		return true
	})
	if !found {
		return fmt.Errorf("%s must call shardgroups.Enable before opening stream handles", path)
	}
	return nil
}

func TestShardableStreamAreasEnableRouting(t *testing.T) {
	allow := map[string]string{
		"event-processing": "resolved-events reads need per-group ack floors, snapshots and replay",
	}
	areas := map[string]bool{}
	for _, s := range streams.All {
		if s.Suffix != streams.InboundEvents && s.Suffix != streams.ResolvedEvents {
			continue
		}
		for _, area := range s.Writers {
			areas[area] = true
		}
		for _, r := range s.Readers {
			areas[r.Area] = true
		}
	}
	if len(areas) == 0 {
		t.Fatal("no shardable stream areas found")
	}
	for area, reason := range allow {
		if !areas[area] || strings.TrimSpace(reason) == "" {
			t.Errorf("stale or unexplained routing exception for %s", area)
		}
	}
	root := workspaceRoot(t)
	for area := range areas {
		t.Run(area, func(t *testing.T) {
			path := filepath.Join(root, "backend", "services", area, "main.go")
			// Parse even allowlisted sources: a missing main must always fail closed.
			err := checkShardGroupEnable(path, nil)
			if _, exempt := allow[area]; exempt && err != nil && strings.Contains(err.Error(), "must call shardgroups.Enable") {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShardGroupEnableScan(t *testing.T) {
	for _, src := range []string{
		"package main; func main() {}",
		"package main; // shardgroups.Enable(nmgr)\nfunc main() {}",
		`package main; import shardgroups "example.com/unrelated"; func main() { shardgroups.Enable(nil) }`,
	} {
		if err := checkShardGroupEnable("main.go", src); err == nil {
			t.Fatalf("accepted a fixture without routing: %s", src)
		}
	}
	if err := checkShardGroupEnable("main.go", `package main; import sg "github.com/devicechain-io/dc-microservice/shardgroups"; func main() { sg.Enable(nil) }`); err != nil {
		t.Fatal(err)
	}
	if err := checkShardGroupEnable(filepath.Join(t.TempDir(), "missing", "main.go"), nil); err == nil {
		t.Fatal("accepted missing main.go")
	}
}
