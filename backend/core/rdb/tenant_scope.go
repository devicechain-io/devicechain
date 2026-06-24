// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// tenantFieldName is the Go struct field name contributed by the embedded
// TenantScoped type. A schema that contains this field is tenant-scoped.
const tenantFieldName = "TenantId"

// RegisterTenantScoping installs global GORM callbacks that enforce per-tenant
// row-level isolation for any model whose schema embeds TenantScoped (i.e.
// exposes a TenantId field). Isolation is un-skippable by construction: it is
// applied once here, not at each call site.
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
// Timing note (GORM v1.31.1): for the *Before* hooks the statement schema is
// already parsed by the time our callback runs, because processor.Execute()
// calls stmt.Parse(stmt.Model) before invoking the registered callback funcs.
// As a defensive measure we still re-parse from Model/Dest if Schema is nil so
// scoping is never silently skipped on a tenant-scoped model. The isolation
// test (tenant_scope_test.go) is the standing guarantee this still holds.
func RegisterTenantScoping(db *gorm.DB) error {
	// Query / row / update / delete inject the tenant predicate; create stamps it.
	for _, register := range []func() error{
		func() error { return db.Callback().Query().Before("gorm:query").Register("dc:tenant_query", tenantScopeQuery) },
		func() error { return db.Callback().Row().Before("gorm:row").Register("dc:tenant_row", tenantScopeQuery) },
		func() error { return db.Callback().Update().Before("gorm:update").Register("dc:tenant_update", tenantScopeQuery) },
		func() error { return db.Callback().Delete().Before("gorm:delete").Register("dc:tenant_delete", tenantScopeQuery) },
		func() error { return db.Callback().Create().Before("gorm:create").Register("dc:tenant_create", tenantScopeCreate) },
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

// scopedTenant returns the context tenant when db is a live, tenant-scoped
// statement. ok=false means the callback should return without acting; any
// fail-closed error (no tenant in context for a tenant-scoped model) has already
// been recorded on db.
func scopedTenant(db *gorm.DB) (string, bool) {
	if db.Error != nil || !isTenantScoped(db) {
		return "", false
	}
	// A deliberate system context (core.WithSystemContext) runs unscoped: no
	// predicate is injected and the fail-closed check is skipped. This is the
	// sanctioned bypass for bootstrap operations that must run before a tenant
	// is known (e.g. the login lookup). See core.WithSystemContext for the
	// security contract governing its use.
	if core.IsSystemContext(db.Statement.Context) {
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
