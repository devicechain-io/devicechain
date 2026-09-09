// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httptransport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot is backend/core relative to this package's directory, which is where `go
// test` runs. The whole module is scanned rather than just the three packages that
// prompted this, because the failure mode is a client added LATER: an author who writes
// &http.Client{Timeout: x} gets http.DefaultTransport, and nothing about the resulting
// code looks wrong.
const moduleRoot = ".."

// No production HTTP client in this module may leave Transport unstated.
//
// Leaving it out is not "no transport" — it is http.DefaultTransport, with the
// environment's proxy configuration, one process-wide connection pool at two idle
// connections per host, and whatever any other package in the process has done to that
// global.
//
// The scan is over non-test files only. A test that dials an httptest server on the
// default transport is dialling loopback and shares nothing that matters.
//
// To satisfy this, state the field: httptransport.New() for platform-internal calls, or
// egress.Guard.Transport() for anything reaching a URL the platform did not choose.
func TestNoProductionHTTPClientLeavesTransportUnstated(t *testing.T) {
	fset := token.NewFileSet()
	var offenders []string
	scanned := 0

	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored or fixture trees are not this module's own source.
			name := d.Name()
			hidden := strings.HasPrefix(name, ".") && name != "." && name != ".."
			if name == "vendor" || name == "testdata" || hidden {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		// The import may be aliased; find whatever name net/http is bound to here.
		httpName, ok := netHTTPName(file)
		if !ok {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isSelector(lit.Type, httpName, "Client") {
				return true
			}
			if !statesTransport(lit) {
				offenders = append(offenders, fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", moduleRoot, err)
	}

	// A walk that found nothing to parse would report no offenders and look identical
	// to a clean tree.
	if scanned < 50 {
		t.Fatalf("scanned only %d non-test .go files under %s; the walk is not reaching the module",
			scanned, moduleRoot)
	}
	// And a scan that cannot recognise the pattern at all would also report none, so
	// prove it recognises one. The literal below is the shape being looked for.
	if !recognisesUnstatedTransport(t) {
		t.Fatal("the scanner does not flag an &http.Client{} with no Transport, so a clean " +
			"result from it means nothing")
	}

	for _, o := range offenders {
		t.Errorf("%s: http.Client built with no Transport, which resolves to http.DefaultTransport; "+
			"state one (httptransport.New() for platform-internal calls, egress.Guard.Transport() "+
			"for tenant-supplied URLs)", o)
	}
}

// recognisesUnstatedTransport is the scanner's negative control: it runs the same
// detection over a source fragment that must be flagged, so a detector broken into
// always-passing (a changed field name, a selector that no longer matches) is caught
// here rather than showing up as a green tree.
func recognisesUnstatedTransport(t *testing.T) bool {
	t.Helper()
	const src = `package p

import "net/http"

var bad = &http.Client{Timeout: 0}
var alsoBad = &http.Client{Transport: nil}
var good = &http.Client{Transport: http.DefaultTransport}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fragment.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the control fragment: %v", err)
	}
	httpName, ok := netHTTPName(file)
	if !ok {
		t.Fatal("the control fragment imports net/http and the import scan did not find it")
	}
	var flagged int
	var total int
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isSelector(lit.Type, httpName, "Client") {
			return true
		}
		total++
		if !statesTransport(lit) {
			flagged++
		}
		return true
	})
	return total == 3 && flagged == 2
}

// netHTTPName reports the name net/http is bound to in this file, honouring an alias.
// A dot-import would put Client in scope unqualified; none exists in this module, and
// this reports not-imported for one rather than guessing.
func netHTTPName(file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != `"net/http"` {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return "", false
			}
			return imp.Name.Name, true
		}
		return "http", true
	}
	return "", false
}

// isSelector reports whether expr is pkg.name, allowing for the &T{} form's type being
// the selector itself.
func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// statesTransport reports whether the literal names a Transport with a value that is not
// the untyped nil. An explicit nil is the same inheritance as omitting the field, so it
// is not a statement of intent this gate accepts.
func statesTransport(lit *ast.CompositeLit) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Transport" {
			continue
		}
		if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "nil" {
			return false
		}
		return true
	}
	return false
}
