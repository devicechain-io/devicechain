// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// tenantFieldName is the Go struct field name contributed by the embedded
// TenantScoped type. A schema that contains this field is tenant-scoped.
const tenantFieldName = "TenantId"

// ErrUnscopedStatement is the refusal for a statement this callback cannot classify:
// one that names a table but whose destination gorm could not parse into a schema, so
// there is no TenantId field to look for and no way to tell whether a predicate is
// owed. It names both ways out, because a developer meeting it needs to know which of
// the two situations they are in rather than which line to delete.
var ErrUnscopedStatement = errors.New("tenant isolation could not be applied: gorm built no schema for this " +
	"statement, so it cannot be told whether the table is tenant-scoped. Give the statement a typed " +
	"destination (Model(&T{}), Find(&[]T{}), Create(&T{})) so the tenant predicate can be injected, or run " +
	"it under core.WithSystemContext if it is genuinely instance-scoped — schema migration is, including " +
	"gorm's own AutoMigrate, which reads an existing table's columns through a statement of exactly this shape")

// RegisterTenantScoping installs global GORM callbacks that enforce per-tenant
// row-level isolation for any model whose schema embeds TenantScoped (i.e.
// exposes a TenantId field). It is applied once here, not at each call site.
//
//   - Query / Row / Update / Delete: a "WHERE tenant_id = ?" predicate is
//     injected from the tenant in db.Statement.Context. A missing tenant aborts
//     the statement with core.ErrNoTenant (fail-closed) so that an unscoped read
//     can never leak another tenant's rows.
//   - Create: the tenant from context is stamped onto every row (struct, slice,
//     or array). A missing tenant aborts with core.ErrNoTenant.
//
// Models without a TenantId field (e.g. migration bookkeeping tables) pass
// through untouched.
//
// 🔴 WHAT IT DOES NOT COVER, said here rather than left to be discovered — the same
// sentence tenant_fence.go writes about the erasure fence, because it is the same gap.
// APPLICABILITY IS A PROPERTY OF THE DESTINATION TYPE, NOT OF THE CALL. gorm decides
// what a statement is by parsing Statement.Model or Statement.Dest into a schema, and
// these callbacks can only ask a schema whether it carries TenantId. Two shapes never
// build one:
//
//   - Raw SQL (Exec, and Raw with the SQL already written). Exec runs on gorm's Raw
//     processor, which has no callbacks registered on it at all, and a statement whose
//     SQL is already built has no clause list left to inject a predicate into. Isolation
//     is therefore a property of the ORM path, not of the database. tenantpurge's sweep
//     is raw Exec, which is why it still works.
//   - A destination gorm cannot parse — the classic being a bare Table(...) with a map,
//     which is the natural way to spell a bulk update. This one used to proceed with no
//     predicate and no error, which is the failure mode this doc previously denied.
//
// The second shape is now REFUSED rather than run: a statement that names a table but
// carries no schema records ErrUnscopedStatement (see scopedTenant). The first is not,
// and cannot usefully be — a raw statement is out of reach of the clause builder, so
// refusing it here would only move the same blind spot to a different error message.
//
// Timing note (GORM v1.31.x): for the *Before* hooks the statement schema is
// already parsed by the time our callback runs, because processor.Execute()
// calls stmt.Parse(stmt.Model) before invoking the registered callback funcs.
// As a defensive measure we still re-parse from Model/Dest if Schema is nil so
// scoping is never silently skipped on a tenant-scoped model. The isolation
// test (tenant_scope_test.go) is the standing guarantee this still holds.
func RegisterTenantScoping(db *gorm.DB) error {
	// Query / row / update / delete inject the tenant predicate; create stamps it.
	for _, register := range []func() error{
		func() error {
			return db.Callback().Query().Before("gorm:query").Register("dc:tenant_query", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Row().Before("gorm:row").Register("dc:tenant_row", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").Register("dc:tenant_update", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Delete().Before("gorm:delete").Register("dc:tenant_delete", tenantScopeQuery)
		},
		func() error {
			return db.Callback().Create().Before("gorm:create").Register("dc:tenant_create", tenantScopeCreate)
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// ensureSchema makes sure db.Statement.Schema is populated. Returns true when a
// usable schema is available after the attempt. Find/First set Dest; Model(...)
// sets Model. The trailing check is authoritative.
//
// It is deliberately free of side effects: the token-grammar and erasure-fence
// callbacks use it too, and for them a statement with no schema is simply out of
// scope. Turning "no schema" into a refusal is this file's judgement, made in
// scopedTenant where the tenant predicate is owed, not here.
func ensureSchema(db *gorm.DB) bool {
	if db.Statement.Schema != nil {
		return true
	}
	if src := db.Statement.Model; src != nil {
		_ = db.Statement.Parse(src)
	} else if src := db.Statement.Dest; src != nil {
		_ = db.Statement.Parse(src)
	}
	return db.Statement.Schema != nil
}

// isTenantScoped reports whether the statement's schema embeds TenantScoped.
func isTenantScoped(db *gorm.DB) bool {
	if !ensureSchema(db) {
		return false
	}
	_, ok := db.Statement.Schema.FieldsByName[tenantFieldName]
	return ok
}

// namesATableWithoutASchema reports the one case this callback must refuse rather than
// wave through: gorm could not build a schema, but the statement DOES name a table, so
// it is about to write or read real rows with no predicate and nothing to say so.
//
// The two exclusions are what keep the refusal to that case:
//
//   - SQL already built (Raw). There is no clause list left to add a predicate to, so a
//     refusal here would not be isolation, it would be an outage on every raw read. Raw
//     is documented as out of reach on RegisterTenantScoping instead.
//   - No table at all. gorm itself already errors on that one — processor.Execute turns
//     an unparseable destination with no table into "Table not set, please set it like:
//     db.Model(&user) or db.Table(\"users\")" — so re-reporting it would replace a
//     precise message with a vaguer one.
func namesATableWithoutASchema(db *gorm.DB) bool {
	stmt := db.Statement
	if stmt.Schema != nil || stmt.SQL.Len() > 0 {
		return false
	}
	return stmt.Table != "" || stmt.TableExpr != nil
}

// scopedTenant returns the context tenant when db is a live, tenant-scoped
// statement. ok=false means the callback should return without acting; any
// fail-closed error (no tenant in context for a tenant-scoped model, or a statement
// that cannot be classified at all) has already been recorded on db.
func scopedTenant(db *gorm.DB) (string, bool) {
	if db.Error != nil {
		return "", false
	}
	// A deliberate system context (core.WithSystemContext) runs unscoped: no
	// predicate is injected and the fail-closed check is skipped. This is the
	// sanctioned bypass for bootstrap operations that must run before a tenant
	// is known (e.g. the login lookup). See core.WithSystemContext for the
	// security contract governing its use.
	//
	// 🔑 IT IS TESTED FIRST, BEFORE CLASSIFICATION, and the order is load-bearing.
	// The unclassifiable-statement refusal below names WithSystemContext as one of its
	// two ways out, so a system context has to reach it. It is also what the migration
	// path relies on: gorm's own migrator reads a table's columns with
	// Table(name).Limit(1).Rows(), and gormigrate reads and writes its bookkeeping
	// table the same way — statements with no schema, all of them issued from inside a
	// library rather than from a call site anyone here could retype. rdb.go runs the
	// whole migration under a system context, which is what makes them legal.
	if core.IsSystemContext(db.Statement.Context) {
		return "", false
	}
	if !isTenantScoped(db) {
		// Not tenant-scoped, or not classifiable. Only the second is a problem, and
		// only when a table is named — see namesATableWithoutASchema.
		if namesATableWithoutASchema(db) {
			_ = db.AddError(fmt.Errorf("%w (table %q)", ErrUnscopedStatement, db.Statement.Table))
		}
		return "", false
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok {
		_ = db.AddError(core.ErrNoTenant)
		return "", false
	}
	return tenant, true
}

// tenantScopeQuery injects the tenant predicate for read/update/delete-style
// statements and fails closed when no tenant is present.
func tenantScopeQuery(db *gorm.DB) {
	tenant, ok := scopedTenant(db)
	if !ok {
		return
	}
	db.Statement.AddClause(clause.Where{
		Exprs: []clause.Expression{
			clause.Eq{
				Column: clause.Column{Table: db.Statement.Table, Name: "tenant_id"},
				Value:  tenant,
			},
		},
	})
}

// tenantScopeCreate stamps the tenant id onto every row being created (SetColumn
// handles struct, slice and array destinations, i.e. batch inserts) and fails
// closed when no tenant is present.
func tenantScopeCreate(db *gorm.DB) {
	tenant, ok := scopedTenant(db)
	if !ok {
		return
	}
	db.Statement.SetColumn(tenantFieldName, tenant, true)
}
