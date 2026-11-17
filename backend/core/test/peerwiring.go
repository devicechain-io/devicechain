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
	"strconv"
	"strings"
)

type peerWiringCount struct{ urls, mints int }

func peerWiringUnder(root string) (map[string]peerWiringCount, error) {
	counts := map[string]peerWiringCount{}
	err := filepath.WalkDir(filepath.Join(root, "backend"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "backend/core/svcclient/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = p
		}
		count := peerWiringCount{}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if imports[id.Name] == mintModule+"svcclient" && sel.Sel.Name == "New" {
				count.mints++
			}
			if imports[id.Name] == "fmt" && sel.Sel.Name == "Sprintf" && len(call.Args) > 0 {
				if value, ok := mintLiteral(call.Args[0]); ok && value == "http://%s:%d/graphql" {
					count.urls++
				}
			}
			return true
		})
		if count != (peerWiringCount{}) {
			counts[rel] = count
		}
		return nil
	})
	return counts, err
}

func checkPeerWiring(found, ledger map[string]peerWiringCount) []string {
	files := map[string]bool{}
	for file := range found {
		files[file] = true
	}
	for file := range ledger {
		files[file] = true
	}
	var problems []string
	for file := range files {
		got, want := found[file], ledger[file]
		if got.urls != want.urls {
			problems = append(problems, fmt.Sprintf("%s: urls got %d, ledger %d", file, got.urls, want.urls))
		}
		if got.mints != want.mints {
			problems = append(problems, fmt.Sprintf("%s: mints got %d, ledger %d", file, got.mints, want.mints))
		}
	}
	sort.Strings(problems)
	return problems
}
