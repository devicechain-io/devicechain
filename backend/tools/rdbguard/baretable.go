// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbguard

import (
	"go/ast"
	"go/token"
)

// bareTableAllowList is the set of non-test files permitted to name a table as a string.
//
// It is EMPTY, and that is a measurement rather than an aspiration: no non-test file in
// this repository calls .Table today. The mechanism is here because the legitimate users
// this would eventually need — code that opens a handle with no callbacks registered at
// all, or that runs under a deliberate system context — are real, and the alternative to
// an allow-list is a pattern that tries to recognise them, which is a pattern anything
// else can be written to look like.
//
// 🔴 An entry added here is not a formality. The callbacks it steps around are the tenant
// predicate and the erasure fence, so an entry needs the same reasoning
// core.WithSystemContext demands, written next to it in Why.
var bareTableAllowList []allowEntry

// BareTableScan reports every non-test file under the roots that calls a method named
// Table — `db.Table("widgets")` and every receiver it can be reached through.
//
// # What the rule protects
//
// The tenant-scope callback classifies a statement from its parsed schema. Naming a table
// as a string produces one of two outcomes, and neither is a scoped read:
//
//   - No parseable destination at all. The statement names a table and carries no schema,
//     which the callback now refuses outright rather than running unscoped. That refusal
//     is the safety net, not the rule — a refusal is an outage, discovered in production.
//   - A destination that DOES parse, into a shape with no TenantId field: a projection
//     struct, or a mismatched Model(&gadget{}).Table("widgets"). That is not
//     unclassifiable, it is classified as NOT tenant-scoped. No predicate is injected,
//     nothing errors, and the read returns every tenant's rows.
//
// The second is the one worth a gate. It has no failure mode at the call site at all: err
// is nil and the rows look right. Both shapes have to start with the same call, which is
// why one check covers them together, and why it is placed at the call rather than at the
// classification.
//
// # What it cannot see
//
//   - A method VALUE: `f := db.Table` and a later `f("widgets")`. Closing this would mean
//     flagging every non-call `.Table` selector, and `stmt.Table` — gorm's own string
//     field — is read in around seventy places across the migrations and core/rdb. An
//     allow-list that large is not read by anyone, and it would go stale on every new
//     migration, so the guard would be enforcing whatever nobody had gotten around to
//     exempting. The narrow true claim is preferred to the broad false one.
//   - Raw SQL. `db.Raw("SELECT * FROM widgets")` and `db.Exec` name no table to the
//     callback and never had a schema; they are documented as out of reach on
//     RegisterTenantScoping and are a different rule.
//   - A helper OUTSIDE the roots that takes a table name and does the call. The roots are
//     two broad directories for exactly this reason.
//   - A gorm handle built with no callbacks registered — a plain gorm.Open — which is
//     unscoped whatever it calls. That is a property of the handle, not of the statement,
//     and it is not a syntactic question.
//   - _test.go files, which are SKIPPED. Tests name tables on purpose: core/rdb's own
//     suite does it to prove the refusal fires, and tenantpurge counts fence rows that way.
//
// In the other direction, this matches the METHOD NAME rather than gorm's type, so a
// zero-argument `Table()` on some unrelated receiver would be reported too. None exists in
// this tree; if one arrives, the allow-list is the answer, and over-reporting a name that
// is not the hazard is the right way round for a guard to be wrong.
//
// Not holes, and listed so nobody re-tests them: the receiver's NAME is irrelevant, since
// the match is on the method rather than on `db` or `tx`; a call split across lines is one
// node in the AST; a chained builder (`db.WithContext(ctx).Session(...).Table(...)`) is the
// same selector; and a comment quoting `db.Table("users")` — core/rdb/tenant_scope.go
// quotes gorm's own error text — is not a node at all, which is the concrete reason this
// is a parser and not a grep.
func BareTableScan(roots ...string) (Result, error) {
	return scan(scanBareTable, roots...)
}

func scanBareTable(fset *token.FileSet, file *ast.File, rel string, res *Result) []Finding {
	var out []Finding

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Table" {
			return true
		}
		for _, e := range bareTableAllowList {
			if matchesAllowPath(rel, e.Path) {
				res.Allowed[e.Path+":Table"]++
				return true
			}
		}
		out = append(out, Finding{
			Pos:     fset.Position(sel.Sel.Pos()),
			Message: "names a table as a string, which reads or writes without a tenant predicate",
			Source:  exprText(sel) + "(…)",
		})
		return true
	})

	return out
}
