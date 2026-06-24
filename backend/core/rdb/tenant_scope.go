/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package rdb

import (
	"reflect"

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
	if err := db.Callback().Query().Before("gorm:query").Register("dc:tenant_query", tenantScopeQuery); err != nil {
		return err
	}
	if err := db.Callback().Row().Before("gorm:row").Register("dc:tenant_row", tenantScopeQuery); err != nil {
		return err
	}
	if err := db.Callback().Update().Before("gorm:update").Register("dc:tenant_update", tenantScopeQuery); err != nil {
		return err
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("dc:tenant_delete", tenantScopeQuery); err != nil {
		return err
	}
	if err := db.Callback().Create().Before("gorm:create").Register("dc:tenant_create", tenantScopeCreate); err != nil {
		return err
	}
	return nil
}

// ensureSchema makes sure db.Statement.Schema is populated. Returns true when a
// usable schema is available after the attempt.
func ensureSchema(db *gorm.DB) bool {
	if db.Statement.Schema != nil {
		return true
	}
	// Attempt to parse from whatever the statement has set. Find/First set Dest;
	// Model(...) sets Model. Parsing populates db.Statement.Schema.
	if db.Statement.Model != nil {
		if err := db.Statement.Parse(db.Statement.Model); err == nil && db.Statement.Schema != nil {
			return true
		}
	}
	if db.Statement.Dest != nil {
		if err := db.Statement.Parse(db.Statement.Dest); err == nil && db.Statement.Schema != nil {
			return true
		}
	}
	return false
}

// isTenantScoped reports whether the statement's schema embeds TenantScoped.
func isTenantScoped(db *gorm.DB) bool {
	if !ensureSchema(db) {
		return false
	}
	_, ok := db.Statement.Schema.FieldsByName[tenantFieldName]
	return ok
}

// tenantScopeQuery injects the tenant predicate for read/update/delete-style
// statements and fails closed when no tenant is present.
func tenantScopeQuery(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	if !isTenantScoped(db) {
		return
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok {
		_ = db.AddError(core.ErrNoTenant)
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

// tenantScopeCreate stamps the tenant id onto every row being created and fails
// closed when no tenant is present.
func tenantScopeCreate(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	if !isTenantScoped(db) {
		return
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok {
		_ = db.AddError(core.ErrNoTenant)
		return
	}
	field := db.Statement.Schema.FieldsByName[tenantFieldName]
	if field == nil {
		return
	}

	rv := db.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				if elem.IsNil() {
					continue
				}
				elem = elem.Elem()
			}
			if err := field.Set(db.Statement.Context, elem, tenant); err != nil {
				_ = db.AddError(err)
				return
			}
		}
	case reflect.Struct:
		if err := field.Set(db.Statement.Context, rv, tenant); err != nil {
			_ = db.AddError(err)
			return
		}
	}
}
