// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"go/ast"
	"go/token"
	"strings"
)

// indexHelpers are core/rdb's partial-unique-index helpers. Two of them
// (CreateTenantTokenIndex, CreateTenantExternalIdIndex) are documented in their own
// source as TEST FIXTURES with no non-test callers; the third
// (CreatePartialUniqueIndex) has exactly one, inside core's own secrets migration.
//
// 🔴 THE RULE IS "NO NON-TEST CALLER", NOT "NO CALLER FROM A MIGRATION", and the
// difference is deliberate. Deciding whether a file is a migration is a classification,
// and the classification is then the thing to evade: rename the file, move the
// gormigrate step into a helper next to it, build the chain from a slice assembled
// elsewhere. There is nothing legitimate a non-test caller of these three does anyway, so
// the guard asks the question that has no gradient in it.
//
// What the rule protects: an index NAME and its WHERE predicate are schema. A migration
// that takes either from another module is silently rewritten when that module changes.
// Fresh installs then build one index and every existing database keeps another, both
// from a diff that never touched the migration, the service, or even the module — and
// both report a clean migration. Services that need this index therefore each declare
// their own copy inside their own migration against their own snapshot struct, and four
// of them carry a comment saying that copy is deliberate.
var indexHelpers = map[string]string{
	"CreatePartialUniqueIndex":    "names rdb.CreatePartialUniqueIndex outside a test",
	"CreateTenantTokenIndex":      "names rdb.CreateTenantTokenIndex outside a test",
	"CreateTenantExternalIdIndex": "names rdb.CreateTenantExternalIdIndex outside a test",
}

// allowEntry exempts one FILE for a named set of SYMBOLS.
//
// 🔴 PER FILE, NOT PER PACKAGE, AND THAT IS THE POINT. The obvious way past a
// package-scoped exemption is a new file in the exempted package: a thin
// `func CreateTenantTokenIndexFor(tx *gorm.DB, m any) error` in core/rdb, which a service
// migration may then call while naming none of the watched symbols itself. Scoped to the
// file, the wrapper is reported where it is DEFINED, which is the only place the guard
// can still see it.
type allowEntry struct {
	// Path is repo-relative and matched EXACTLY against the walked path — see
	// matchesAllowPath for why a suffix match would be an exemption anyone can claim by
	// choosing a directory name.
	Path string
	// Symbols are the watched names this file may name. Every one of them MUST be found:
	// see the liveness note on Result.Allowed.
	Symbols []string
	Why     string
}

// indexHelperAllowList is the complete set of non-test files permitted to name a helper.
//
// 🔴 EVERY ENTRY IS ALSO A LIVENESS PROBE. A scan that stopped matching — a renamed
// symbol, a walker that stopped reaching backend/core, a filter that silently excluded
// everything — reports zero findings, which is exactly what a clean tree reports. The
// difference is that a working scan also lands on each entry below at least once. So an
// entry that absorbs nothing is a HARD FAILURE of the instrument, not a tidy-up: it means
// either the guard has gone blind, or the one legitimate call it was built around has
// moved and nobody re-derived the rule.
var indexHelperAllowList = []allowEntry{
	{
		Path: "backend/core/rdb/token_index.go",
		Symbols: []string{
			"CreatePartialUniqueIndex",
			"CreateTenantTokenIndex",
			"CreateTenantExternalIdIndex",
		},
		Why: "the helpers' own definitions, and the intra-package call between them",
	},
	{
		Path:    "backend/core/secrets/migration.go",
		Symbols: []string{"CreatePartialUniqueIndex"},
		Why: "the one sanctioned caller: core's own secrets migration, which passes a locally " +
			"declared snapshot struct and a literal index name, and whose emitted statement is " +
			"pinned by frozen-SQL tests on both sides",
	},
}

// IndexHelperScan reports every non-test file under the roots that names one of
// core/rdb's index helpers and is not on the allow-list.
//
// 🔴 WHAT IT CANNOT SEE. Written out rather than left for a reviewer to find, because a
// guard is worth its coverage and not its green tick:
//
//   - A migration that RE-IMPLEMENTS the statement instead of calling the helper. That is
//     not an evasion, it is the sanctioned pattern — six areas do it today — and this
//     guard has no opinion about it. What keeps those copies honest is the migration-diff
//     golden schema, which is a different instrument.
//   - A wrapper defined INSIDE an allow-listed file. token_index.go may add a function
//     that calls CreatePartialUniqueIndex and export it, and a service migration calling
//     that wrapper names nothing watched. Narrowing the allow-list to a file rather than a
//     package is what keeps this down to two files a reviewer must actually read.
//   - The symbol reached without ever being named in source: reflection on a string built
//     at run time, or //go:linkname. Both are recorded as impractical rather than
//     unnoticed — reflection cannot obtain a package-level func value at all here, and
//     //go:linkname to a non-runtime symbol is refused by the linker in a real build.
//   - Anything outside the roots the caller passes, including the module cache and any Go
//     tree added to this repository later. The roots are two broad directories rather than
//     an enumeration of modules for that reason.
//   - _test.go files, which are SKIPPED — the helpers exist for them.
//   - testdata directories, and the directories listed on scan().
//
// Three shapes are NOT holes, and are listed so nobody re-tests them: an import alias, a
// dot-import and a call split across lines. Matching is on the SELECTOR and on the bare
// IDENTIFIER, which is to say on the symbol's own name, so the local name bound to
// core/rdb never enters into it — and an AST has no line breaks. A build-tagged or
// generated file is parsed like any other, because this reads syntax rather than a build
// configuration; it therefore over-reports rather than under-reports. A //nolint comment
// has no effect at all.
func IndexHelperScan(roots ...string) (Result, error) {
	return scan(scanIndexHelpers, roots...)
}

func scanIndexHelpers(fset *token.FileSet, file *ast.File, rel string, res *Result) []Finding {
	var out []Finding

	// allowed reports whether this file may name sym, and records the hit against the
	// allow-list entry so a stale entry can be detected.
	allowed := func(sym string) bool {
		for _, e := range indexHelperAllowList {
			if !matchesAllowPath(rel, e.Path) {
				continue
			}
			for _, s := range e.Symbols {
				if s == sym {
					res.Allowed[e.Path+":"+s]++
					return true
				}
			}
		}
		return false
	}

	record := func(pos token.Pos, sym, source string) {
		if allowed(sym) {
			return
		}
		out = append(out, Finding{
			Pos:     fset.Position(pos),
			Message: indexHelpers[sym],
			Source:  source,
		})
	}

	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			// 🔴 MATCHED WHEREVER THE NAME APPEARS, NOT ONLY AT A CALL.
			// `var install = rdb.CreateTenantTokenIndex` never calls anything here, and
			// the call through `install` is then invisible to anything looking for a call
			// expression.
			if _, watched := indexHelpers[node.Sel.Name]; watched {
				record(node.Sel.Pos(), node.Sel.Name, exprText(node))
				// Report the selector once, then keep walking the receiver so a nested
				// match inside it is not swallowed. Returning true here instead would
				// re-report node.Sel as a bare identifier.
				ast.Inspect(node.X, visit)
				return false
			}
		case *ast.Ident:
			// The bare form. This is what a dot-import of core/rdb produces at a call
			// site, what an intra-package call looks like, and what a function DECLARED
			// with one of these names looks like — the last being how a same-named
			// wrapper in another package is caught at its definition.
			if _, watched := indexHelpers[node.Name]; watched {
				record(node.Pos(), node.Name, node.Name)
			}
		}
		return true
	}
	ast.Inspect(file, visit)

	return out
}

// exprText renders a selector chain for the report — `rdb.CreateTenantTokenIndex`,
// `db.WithContext(…).Table`. Calls in the chain are rendered as `(…)` rather than
// expanded, because the argument that matters is at the end and the receiver only needs
// to be recognisable in an error message.
//
// It falls back to the selector alone for anything it cannot render, which is the part
// that identifies the finding; the file and line say where. A report that named only
// `Table` was the first draft, and it was worse in the one place a reader uses it: a
// chained builder is the common spelling, so the useful half of the expression is
// everything the fallback threw away.
func exprText(sel *ast.SelectorExpr) string {
	var parts []string
	var walk func(ast.Expr) bool
	walk = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.Ident:
			parts = append(parts, x.Name)
			return true
		case *ast.SelectorExpr:
			if !walk(x.X) {
				return false
			}
			parts = append(parts, x.Sel.Name)
			return true
		case *ast.CallExpr:
			if !walk(x.Fun) || len(parts) == 0 {
				return false
			}
			parts[len(parts)-1] += "(…)"
			return true
		case *ast.ParenExpr:
			return walk(x.X)
		case *ast.StarExpr:
			return walk(x.X)
		}
		return false
	}
	if !walk(sel) {
		return sel.Sel.Name
	}
	return strings.Join(parts, ".")
}

// IndexHelperAllowKeys is every (file, symbol) pair the allow-list declares, in the form
// Result.Allowed is keyed by. A run that did not land on all of them has either gone
// blind or is enforcing a rule derived from a tree that has since moved.
func IndexHelperAllowKeys() []string { return allowKeys(indexHelperAllowList) }

// BareTableAllowKeys is the same for the bare-table allow-list, which is empty today —
// so this returns nothing, and the bare-table check's only liveness evidence is the
// per-root file floor and its self-test. Said plainly because the two checks are not
// equally well instrumented, and a reader is entitled to know which is which.
func BareTableAllowKeys() []string { return allowKeys(bareTableAllowList) }

func allowKeys(entries []allowEntry) []string {
	var out []string
	for _, e := range entries {
		if len(e.Symbols) == 0 {
			out = append(out, e.Path+":Table")
			continue
		}
		for _, s := range e.Symbols {
			out = append(out, e.Path+":"+s)
		}
	}
	return out
}
