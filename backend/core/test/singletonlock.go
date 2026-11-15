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

type singletonLockSite struct{ File, Function, Primitive string }

type singletonLockSanction struct {
	Count  int
	Reason string
}

// singletonLockSitesUnder enumerates selector references, including parenthesised
// calls and method values, with closures attributed to their enclosing declaration.
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
				sel, ok := n.(*ast.SelectorExpr)
				if ok && (sel.Sel.Name == "TryAdvisoryLock" || sel.Sel.Name == "TryAdvisoryLockSession") {
					sites = append(sites, singletonLockSite{rel, name, sel.Sel.Name})
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
func singletonLockLedgerErrors(sites []singletonLockSite, ledger map[singletonLockSite]singletonLockSanction) []string {
	found := map[singletonLockSite]int{}
	var issues []string
	for _, site := range sites {
		found[site]++
	}
	for site, count := range found {
		if count != ledger[site].Count {
			issues = append(issues, fmt.Sprintf("try-lock reference count: %s %s %s: got %d, want %d", site.File, site.Function, site.Primitive, count, ledger[site].Count))
		}
	}
	for site, sanction := range ledger {
		if found[site] == 0 {
			issues = append(issues, fmt.Sprintf("stale try-lock caller: %s %s %s", site.File, site.Function, site.Primitive))
		}
		if sanction.Count <= 0 || strings.TrimSpace(sanction.Reason) == "" {
			issues = append(issues, "missing try-lock count or reason")
		}
	}
	sort.Strings(issues)
	return issues
}
