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
	"strings"
)

// sharedCacheParam is the SQLite URI parameter that makes an in-memory database findable
// by NAME. It is assembled rather than written out so that this package's own source does
// not hold the literal its scan refuses.
const sharedCacheParam = "cache=" + "shared"

// SharedCacheDSNOwners are the files allowed to spell out a shared-cache SQLite DSN,
// relative to the workspace root: the helper that builds one with a per-call nonce, and
// the test that pins it.
var SharedCacheDSNOwners = []string{
	"backend/core/rdb/sqlitetest/sqlitetest.go",
	"backend/core/rdb/sqlitetest/sqlitetest_test.go",
}

// sharedCacheLiteral is one string literal holding a shared-cache DSN, and where.
type sharedCacheLiteral struct {
	File string // relative to the scan root, slash-separated
	Pos  string
}

// sharedCacheLiteralsUnder walks root and returns every Go string literal containing a
// shared-cache parameter, split into those in an owner file (allowed) and those anywhere
// else (refused), plus the directories it descended into.
//
// It reads string LITERALS from the parsed file, not lines of text, so a comment that
// talks about the parameter is not a finding and a raw-string fixture that embeds one is.
// The match is case-insensitive because SQLite's is.
//
// 🔴 WHAT IT CANNOT SEE: a DSN assembled from pieces ("cache=" + "shared", or a "%s"
// filled in at run time) never appears as one literal. That is a deliberate evasion, not
// an accident someone copies from a neighbour, and the neighbours are what this guards.
func sharedCacheLiteralsUnder(root string, owners []string) (allowed, refused []sharedCacheLiteral, visited map[string]bool, err error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, nil, err
	}
	owned := map[string]bool{}
	for _, o := range owners {
		owned[filepath.ToSlash(o)] = true
	}
	fset := token.NewFileSet()
	visited = map[string]bool{}
	files := 0
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
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		files++
		parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if !strings.Contains(strings.ToLower(lit.Value), sharedCacheParam) {
				return true
			}
			hit := sharedCacheLiteral{File: rel, Pos: fset.Position(lit.Pos()).String()}
			if owned[rel] {
				allowed = append(allowed, hit)
			} else {
				refused = append(refused, hit)
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if files == 0 {
		return nil, nil, nil, fmt.Errorf("no .go files found under %s, so the scan asserts nothing", abs)
	}
	return allowed, refused, visited, nil
}
