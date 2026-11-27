// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ActivationServed must be a compile-time constant. If it ever became a variable this line would
// not compile, because a constant declaration cannot be initialized from one.
const activationServedIsAConstant = ActivationServed

func parsePackage(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) < 5 {
		t.Fatalf("found only %d source files; the guard is not looking at the package", len(files))
	}
	return fset, files
}

// TestActivationServedCannotBeToggled shows that nothing exported can make this build treat an
// intent as ACTIVE: the flag is a constant declared false; the package exports no variable but
// error sentinels, so there is no state to flip; and every exported path that could accept an
// ACTIVE lifecycle refuses it.
func TestActivationServedCannotBeToggled(t *testing.T) {
	if activationServedIsAConstant {
		t.Fatal("ActivationServed is true")
	}
	fset, files := parsePackage(t)
	declared := false
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name.Name == "ActivationServed" {
						var lit *ast.Ident
						isIdent := false
						if i < len(vs.Values) {
							lit, isIdent = vs.Values[i].(*ast.Ident)
						}
						if gen.Tok != token.CONST || !isIdent || lit.Name != "false" {
							t.Errorf("%s: ActivationServed must be declared `const ActivationServed = false`", fset.Position(name.Pos()))
						}
						declared = true
					}
					if gen.Tok == token.VAR && name.IsExported() && (i >= len(vs.Values) || !isErrorsNew(vs.Values[i])) {
						t.Errorf("%s: exported variable %s is mutable state; only errors.New sentinels may be exported variables",
							fset.Position(name.Pos()), name.Name)
					}
				}
			}
		}
		// No assignment anywhere in the package targets ActivationServed (it could not compile,
		// but a rename to a var plus an assignment is exactly the regression to catch).
		ast.Inspect(f, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "ActivationServed" {
						t.Errorf("%s: ActivationServed is assigned", fset.Position(id.Pos()))
					}
				}
			}
			return true
		})
	}
	if !declared {
		t.Fatal("ActivationServed is not declared in the package")
	}

	if mode, err := PlanMode(LifecycleActive); !errors.Is(err, ErrActivationNotServed) || mode != "" {
		t.Errorf("PlanMode(ACTIVE) = %q, %v; want ErrActivationNotServed", mode, err)
	}
	for l, want := range map[Lifecycle]bool{
		LifecycleDraft: false, LifecycleShadow: true, LifecycleActive: false,
		LifecycleSuspended: false, LifecycleRetired: false, "": false, "active": false,
	} {
		if got := Reconciled(l); got != want {
			t.Errorf("Reconciled(%q) = %v, want %v", l, got, want)
		}
		mode, err := PlanMode(l)
		if want && (err != nil || mode != ModeShadow) {
			t.Errorf("PlanMode(%q) = %q, %v; a reconciled lifecycle records SHADOW in this build", l, mode, err)
		}
		if mode == ModeActive {
			t.Errorf("PlanMode(%q) returned ACTIVE", l)
		}
	}
}

func isErrorsNew(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "errors" && sel.Sel.Name == "New"
}

// TestThePackageIsPure holds the package to no I/O and no clock: the standard library's pure
// packages and core/ledger, and no call that reads or waits on time.
func TestThePackageIsPure(t *testing.T) {
	allowed := map[string]bool{
		"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true,
		"fmt": true, "hash": true, "io": true, "math": true, "slices": true, "sort": true, "strconv": true,
		"strings": true, "time": true, "unicode/utf8": true,
		"github.com/devicechain-io/dc-microservice/ledger": true,
	}
	clock := map[string]bool{"Now": true, "Since": true, "Until": true, "Sleep": true, "After": true,
		"AfterFunc": true, "Tick": true, "NewTicker": true, "NewTimer": true}
	fset, files := parsePackage(t)
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[path] {
				t.Errorf("%s: %s imports %q; core/intent is pure", fset.Position(imp.Pos()), fset.File(f.Pos()).Name(), path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" && clock[sel.Sel.Name] {
				t.Errorf("%s: time.%s reads or waits on a clock; instants are parameters", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
