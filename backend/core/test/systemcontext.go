// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// corePackagePath is the module path of the package declaring WithSystemContext. Call
// sites are matched by resolving the file's IMPORTS to this path rather than by the
// identifier they happen to spell it with: the tree uses both `core` and `dccore`
// today, an alias nobody has used yet would be missed by a name match, and a local
// helper of the same name on an unrelated package would be falsely reported by one.
const corePackagePath = "github.com/devicechain-io/dc-microservice/core"

// systemContextFunc is the bypass being enumerated.
const systemContextFunc = "WithSystemContext"

// packageLevelSite is the Function value recorded for a call that is not inside any
// function declaration — a package-level var initializer. It is spelled as something
// that cannot collide with a Go identifier so a ledger entry can never be confused for
// a real function name.
const packageLevelSite = "(package-level)"

// systemContextSite is one call to core.WithSystemContext: the single sanctioned bypass
// of tenant isolation, which core/core/system.go describes as a place where "every call
// site is a security review point".
//
// That sentence was true as a policy and unenforced as a fact. Nothing enumerated the
// call sites, so a new bypass arrived in a diff looking like any other context plumbing,
// and the review the doc promises happened only if a reviewer already knew to look for
// this one identifier.
type systemContextSite struct {
	File     string // absolute path, carried separately so callers need not parse Pos
	Pos      string // file:line:col, for the failure message
	Function string // "Type.Method", "Func", or packageLevelSite
}

// systemContextSitesUnder walks root and reports every production call to
// core.WithSystemContext, along with the set of directories the walk descended into.
//
// It is separate from the assertion that consumes it so this package's own tests can
// drive it against fixtures and read what it found. A guard whose only output is a
// failed test cannot be shown to FIRE, and for a scanner that matters more than usual:
// its failure mode is reporting clean, which is indistinguishable from the answer a
// correct scan of a correct tree gives.
//
// Test files are skipped deliberately. A test may build a system context to drive the
// callback it is testing — several do, including the fixtures this scanner is proven on
// — and those are not bypasses shipped to anyone.
func systemContextSitesUnder(root string) ([]systemContextSite, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	visited := map[string]bool{}
	var files []string

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != abs && skipDirInScan(d.Name()) {
				return fs.SkipDir
			}
			visited[path] = true
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no non-test .go files found under %s, so the scan asserts nothing", abs)
	}

	var found []systemContextSite
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		found = append(found, systemContextSitesIn(fset, f)...)
	}
	return found, visited, nil
}

// systemContextSitesIn reports the call sites in one parsed file.
//
// Declarations are walked one at a time so the enclosing function is known without
// maintaining an ancestor stack. A call inside a func literal is attributed to the
// function the literal is written in, which is the function a reader opens.
func systemContextSitesIn(fset *token.FileSet, f *ast.File) []systemContextSite {
	names := coreImportNames(f)
	unqualified := f.Name != nil && f.Name.Name == "core" && declaresSystemContext(f)
	if len(names) == 0 && !unqualified {
		return nil
	}

	var found []systemContextSite
	record := func(node ast.Node, fn string) {
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSystemContextCall(call, names, unqualified) {
				return true
			}
			at := fset.Position(call.Pos())
			found = append(found, systemContextSite{File: at.Filename, Pos: at.String(), Function: fn})
			return true
		})
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			// A package-level var initializer can call it too, and a bypass installed
			// there is harder to notice than one in a function, not easier.
			record(decl, packageLevelSite)
			continue
		}
		if fn.Body == nil {
			continue
		}
		record(fn.Body, funcSiteName(fn))
	}
	return found
}

// declaresSystemContext reports whether this file declares WithSystemContext itself,
// which is what makes an unqualified call in the same package a real call to it rather
// than to some same-named function in an unrelated package also called "core".
//
// 🔴 THE SIGNATURE IS CHECKED, NOT JUST THE NAME, and this guard did not do that until
// its own fixture caught it: "package core" plus a function of that name was enough,
// so any unrelated package called core declaring any WithSystemContext at all had every
// unqualified call to it reported as a tenant-isolation bypass. Matching the shape
// actually being enumerated — context.Context in, context.Context out — is what makes
// the package identification a fact rather than a coincidence of naming.
func declaresSystemContext(f *ast.File) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || fn.Name.Name != systemContextFunc {
			continue
		}
		if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
			continue
		}
		if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if isContextContext(fn.Type.Params.List[0].Type) && isContextContext(fn.Type.Results.List[0].Type) {
			return true
		}
	}
	return false
}

// isContextContext reports whether expr is written as context.Context. The package
// qualifier is taken as spelled: inside the declaring package the standard library is
// imported plainly, and a file that aliased it would simply not be recognised here —
// which costs an unqualified call in one package and no correctness anywhere.
func isContextContext(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Context" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "context"
}

// isSystemContextCall reports whether call is a call to core.WithSystemContext, either
// qualified by one of this file's names for the core package or — inside that package
// — unqualified.
func isSystemContextCall(call *ast.CallExpr, names map[string]bool, unqualified bool) bool {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel.Name != systemContextFunc {
			return false
		}
		ident, ok := fun.X.(*ast.Ident)
		return ok && names[ident.Name]
	case *ast.Ident:
		return unqualified && fun.Name == systemContextFunc
	}
	return false
}

// coreImportNames returns every identifier this file binds to the core package: the
// alias when one is given, otherwise the package's own name.
//
// A dot import would bind the package's exported names directly and is NOT handled. It
// does not appear in this tree and would break far more than this scanner, but a reader
// of a clean result should know which shape the clean result did not consider.
func coreImportNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != corePackagePath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				names[imp.Name.Name] = true
			}
			continue
		}
		names["core"] = true
	}
	return names
}

// funcSiteName renders the enclosing function the way the ledger spells it: qualified
// by the receiver type when there is one, because two types in one file routinely carry
// methods of the same name and a bare method name would make two different bypasses
// indistinguishable in the list.
func funcSiteName(fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return name
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name + "." + name
	}
	return name
}
