// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shrink-only: a direct pass recorder must declare its interval in the same package.
// Keys are package-relative metric identities as returned by passIntervalGaps.
var passIntervalExceptions = map[string]string{}

func TestEveryDirectPassRecorderDeclaresItsInterval(t *testing.T) {
	gaps, err := passIntervalGaps(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for key, pos := range gaps {
		seen[key] = true
		if _, allowed := passIntervalExceptions[key]; !allowed {
			t.Errorf("%s: %s calls RecordPass without SetInterval on the same metrics value in its package", pos, key)
		}
	}
	for key := range passIntervalExceptions {
		if !seen[key] {
			t.Errorf("stale passIntervalExceptions entry %s; remove it", key)
		}
	}
}

func TestPassIntervalGuardFindsAnUndeclaredInterval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backend", "services", "fixture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "loop.go")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Another field or a shadowed receiver must not satisfy this loop's obligation.
	// Package globals must also be declared. Comments and test files are not calls.
	write(`// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package fixture
type loop struct { pass, other *Metrics }
func (l *loop) run() {
	l.pass.RecordPass()
	l.other.SetInterval(1)
	func(l *loop) { l.pass.SetInterval(1) }(nil)
}
func unrelated(pass *Metrics) { pass.SetInterval(1) }
var shared *Metrics
func recordGlobal() { shared.RecordPass() }
`)
	gaps, err := passIntervalGaps(root)
	if err != nil || len(gaps) != 2 {
		t.Fatalf("planted missing declarations: got %v, %v; want two gaps", gaps, err)
	}
	for key, pos := range gaps {
		if (!strings.Contains(key, "loop.pass") && !strings.HasSuffix(key, ":shared")) || !strings.Contains(pos, "loop.go") {
			t.Fatalf("did not identify planted metrics value: %s at %s", key, pos)
		}
	}
	// Declarations can be in another method/file, and receiver variable names can differ.
	if err := os.WriteFile(filepath.Join(dir, "start.go"), []byte(`// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package fixture
func (s *loop) start() { s.pass.SetInterval(1) }
func declareGlobal() { shared.SetInterval(1) }
`), 0644); err != nil {
		t.Fatal(err)
	}
	gaps, err = passIntervalGaps(root)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("declared interval should pass: %v, %v", gaps, err)
	}
}

// Scan syntax, not text: comments, strings and test-only calls cannot satisfy a
// declaration. Receiver fields use the receiver type as identity across methods;
// package globals keep their package identity, and parameters and locals keep
// their declaration's identity, including shadowed bindings. This is a wiring guard,
// not alias analysis: pass the metrics value directly at the declaration site.
func passIntervalGaps(root string) (map[string]string, error) {
	recorded, declared := map[string]string{}, map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "backend"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "backend/core/core/") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(rel)) + ":" + file.Name.Name
		globals := map[*ast.Object]bool{}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range value.Names {
							globals[name.Obj] = true
						}
					}
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			receiver, receiverType := "", ""
			var receiverObject *ast.Object
			if fn.Recv != nil && len(fn.Recv.List[0].Names) != 0 {
				receiver = fn.Recv.List[0].Names[0].Name
				receiverObject = fn.Recv.List[0].Names[0].Obj
				receiverType = strings.TrimPrefix(passIntervalExpr(fset, fn.Recv.List[0].Type), "*")
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "RecordPass" && sel.Sel.Name != "SetInterval") {
					return true
				}
				value := passIntervalExpr(fset, sel.X)
				root := passIntervalRoot(sel.X)
				identity := value
				binding := token.NoPos
				if root == nil {
					// A computed receiver cannot prove that two calls use one value.
					binding = sel.X.Pos()
				} else if root.Obj != nil && !globals[root.Obj] && root.Obj.Kind != ast.Pkg {
					binding = root.Obj.Pos()
				}
				if binding.IsValid() {
					pos := fset.Position(binding)
					pos.Filename = filepath.Base(pos.Filename)
					identity += "@" + pos.String()
				}
				if receiver != "" && root != nil && root.Obj == receiverObject && strings.HasPrefix(value, receiver+".") {
					identity = receiverType + strings.TrimPrefix(value, receiver)
				}
				key := pkg + ":" + identity
				if sel.Sel.Name == "SetInterval" {
					declared[key] = true
				} else {
					recorded[key] = fset.Position(call.Pos()).String()
				}
				return true
			})
		}
		return nil
	})
	for key := range recorded {
		if declared[key] {
			delete(recorded, key)
		}
	}
	return recorded, err
}

func passIntervalRoot(expr ast.Expr) *ast.Ident {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr
	case *ast.SelectorExpr:
		return passIntervalRoot(expr.X)
	case *ast.ParenExpr:
		return passIntervalRoot(expr.X)
	case *ast.StarExpr:
		return passIntervalRoot(expr.X)
	default:
		return nil
	}
}

func passIntervalExpr(fset *token.FileSet, expr ast.Expr) string {
	var b bytes.Buffer
	_ = format.Node(&b, fset, expr)
	return b.String()
}
