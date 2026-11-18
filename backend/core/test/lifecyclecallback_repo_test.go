// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// core.LifecycleManager calls a callback's Preprocess and Postprocess unconditionally, so a
// core.LifecycleCallback literal that leaves either one out is a nil function call: the
// component panics the moment that transition runs. A service's main.go transitions are
// driven by no unit test (only a real deploy runs Stop), so this checks the literals instead.
func lifecycleCallbacksMissingAStep(root string) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(filepath.Join(root, "backend/services"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "LifecycleCallback" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "core" {
				return true
			}
			keys := map[string]bool{}
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "nil" {
							keys[k.Name] = true
						}
					}
				}
			}
			for _, step := range []string{"Preprocess", "Postprocess"} {
				if !keys[step] {
					rel, _ := filepath.Rel(root, path)
					problems = append(problems, fmt.Sprintf("%s:%d: core.LifecycleCallback has no %s",
						filepath.ToSlash(rel), fset.Position(lit.Pos()).Line, step))
				}
			}
			return true
		})
		return nil
	})
	return problems, err
}

func TestEveryServiceLifecycleCallbackHasBothSteps(t *testing.T) {
	problems, err := lifecycleCallbacksMissingAStep(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestLifecycleCallbackGuardNegativeControl(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backend/services/planted")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package main

import "github.com/devicechain-io/dc-microservice/core"

var ok = core.LifecycleCallback{Preprocess: nil0, Postprocess: nil0}
var missing = core.LifecycleCallback{Preprocess: nil0}
var explicitNil = core.LifecycleCallback{Preprocess: nil0, Postprocess: nil}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := lifecycleCallbacksMissingAStep(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("want the missing and the explicit-nil Postprocess reported, got %v", problems)
	}
	for _, p := range problems {
		if !strings.Contains(p, "has no Postprocess") {
			t.Fatalf("unexpected report %q", p)
		}
	}
}
