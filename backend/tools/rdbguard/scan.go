// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package rdbguard holds two source checks about how code is allowed to reach the
// relational database, both of which ask a question no single Go module is positioned to
// answer.
//
//   - IndexHelperScan: core/rdb's partial-unique-index helpers must have no non-test
//     callers beyond the one file that is allowed to call one. They are test fixtures. A
//     migration that sources an index NAME and a WHERE predicate from another module
//     starts building a different index on FRESH installs the day that module changes,
//     while every existing database keeps the shape it was given — so both report a
//     clean migration and hold different schemas.
//
//   - BareTableScan: no non-test code may name a table as a string, `db.Table("widgets")`.
//     Such a statement either carries no parseable schema at all — which the tenant-scope
//     callback refuses — or parses into a destination with no TenantId field, which
//     classifies as NOT tenant-scoped, injects no predicate, and returns every tenant's
//     rows with a nil error. Both shapes start with the same call, so refusing the call
//     covers them together.
//
// # Why these parse rather than grep
//
// Both rules are about a NAME, and in this tree the forbidden names are already written
// down in prose. Four service migrations carry a comment saying "this is a deliberate
// copy of rdb.CreateTenantTokenIndex, not an oversight", and core/rdb/tenant_scope.go
// quotes gorm's own error text, which contains `db.Table(\"users\")`. A text scan reports
// all of those and is therefore either wrong on its first run or narrowed with exclusions
// until it is wrong quietly. An AST walk never sees a comment.
//
// The other half is the usual one: the AST has no line breaks, and a selector is evidence
// wherever the symbol is NAMED, whether it is called there or not — so a call split
// across lines, a method value (`var f = rdb.CreateTenantTokenIndex`) and an indirect
// call through that variable are all the same match.
//
// # What both scans share
//
// Non-test .go files only, under caller-supplied roots, with a per-root minimum file
// count. The count is not decoration: a scan that read nothing has no opinion about the
// tree, and "clean" is the wrong way to report that.
package rdbguard

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

// Finding is one place a file breaks the rule being scanned for.
type Finding struct {
	Pos     token.Position
	Message string
	Source  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Message, f.Source)
}

// Result is what a scan saw, including how much it saw. PerRoot is the number of files
// parsed under each root, and Allowed counts the hits each allow-list entry absorbed.
//
// 🔴 A caller that reads Findings and ignores the other two fields cannot tell a clean
// tree from a tree it never read, nor a live allow-list from a stale one. Both are
// absences, and an absence reported as an answer is the failure these guards exist to
// prevent in the code they scan.
type Result struct {
	PerRoot  map[string]int
	Allowed  map[string]int
	Findings []Finding
}

// Files is the total parsed across every root.
func (r Result) Files() int {
	n := 0
	for _, c := range r.PerRoot {
		n += c
	}
	return n
}

// fileScanner reports the findings in one parsed file. rel is the file's path as given to
// the walker, which is what allow-list entries are matched against.
type fileScanner func(fset *token.FileSet, file *ast.File, rel string, res *Result) []Finding

// scan walks each root, parses every non-test .go file, and hands it to fn.
//
// Directories skipped, and why each:
//
//   - _legacy — the archived pre-migration tree. Not in the workspace, not built, not
//     maintained, and it does not run.
//   - testdata — skipped for the reason go build skips it: it may hold source that is
//     deliberately not valid Go, and a parse error here is fatal rather than ignored.
//   - vendor, node_modules — not this repository's source.
//   - .claude — where this repo's git worktrees live. Without this a maintainer with a
//     worktree scans a second, older copy of the whole tree and gets findings at paths
//     that are not the ones they are editing, while CI (which has no worktrees) stays
//     green. A gate that is red for a reason the reader cannot act on is a gate the
//     reader learns to skip.
//
// A parse error is returned, never skipped: a file that could not be read has not been
// checked, and the two must not look the same.
func scan(fn fileScanner, roots ...string) (Result, error) {
	res := Result{PerRoot: map[string]int{}, Allowed: map[string]int{}}
	fset := token.NewFileSet()

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "_legacy", "vendor", "node_modules", "testdata", ".claude":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parsing %s: %w", path, err)
			}
			res.PerRoot[root]++
			res.Findings = append(res.Findings, fn(fset, file, filepath.ToSlash(path), &res)...)
			return nil
		})
		if err != nil {
			return res, err
		}
	}

	sort.Slice(res.Findings, func(i, j int) bool {
		if res.Findings[i].Pos.Filename != res.Findings[j].Pos.Filename {
			return res.Findings[i].Pos.Filename < res.Findings[j].Pos.Filename
		}
		return res.Findings[i].Pos.Line < res.Findings[j].Pos.Line
	})
	return res, nil
}

// matchesAllowPath reports whether the walked path p is the allow-list entry want.
//
// 🔴 EXACT, NOT A SUFFIX, and the difference is a hole rather than a nicety. Allow-list
// entries are repo-relative ("backend/core/secrets/migration.go") and the guards run from
// the repository root over the relative roots `backend` and `deploy`, so the walked path
// IS the entry — there is nothing for suffix matching to buy. What it would cost is an
// exemption anyone can claim by construction: a new file at
// `backend/services/x/backend/core/rdb/token_index.go` ends with the entry and would
// inherit its exemption while being nothing of the kind.
//
// The consequence of exactness is deliberate and fail-closed: a scan rooted somewhere
// else — an absolute path, or a fixture tree — matches no entry, and the liveness check
// then refuses the run rather than reporting it clean. That is the correct answer for a
// scan that is not looking at the repository.
func matchesAllowPath(p, want string) bool {
	return filepath.Clean(p) == filepath.Clean(want)
}
