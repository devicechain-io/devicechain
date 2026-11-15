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
	"sort"
	"strings"
)

type singletonLockSite struct{ File, Function string }

// singletonLockSitesUnder enumerates selector calls, including calls in closures
// attributed to their enclosing declaration. Aliased method values are not caught.
// The primitive's implementation package and test fixtures are excluded.
func singletonLockSitesUnder(root string) ([]singletonLockSite, error) {
	var sites []singletonLockSite
	files := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "backend/core/rdb" || (path != root && skipDirInScan(d.Name())) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		for _, decl := range f.Decls {
			name := packageLevelSite
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = funcSiteName(fn)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (sel.Sel.Name == "TryAdvisoryLock" || sel.Sel.Name == "TryAdvisoryLockSession") {
					sites = append(sites, singletonLockSite{rel, name})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if files == 0 {
		return nil, fmt.Errorf("no production Go files scanned under %s", root)
	}
	return sites, nil
}

// singletonLockLedgerErrors checks both directions, so stale sanctions also fail.
func singletonLockLedgerErrors(sites []singletonLockSite, ledger map[singletonLockSite]string) []string {
	found := map[singletonLockSite]bool{}
	var issues []string
	for _, site := range sites {
		found[site] = true
		if strings.TrimSpace(ledger[site]) == "" {
			issues = append(issues, fmt.Sprintf("unsanctioned try-lock caller: %s %s", site.File, site.Function))
		}
	}
	for site, reason := range ledger {
		if !found[site] {
			issues = append(issues, fmt.Sprintf("stale try-lock caller: %s %s", site.File, site.Function))
		}
		if strings.TrimSpace(reason) == "" {
			issues = append(issues, "missing try-lock reason")
		}
	}
	sort.Strings(issues)
	return issues
}
