// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// The create callback stamps the context's tenant onto the rows it writes and refuses a row
// that names another. The update callback used to add only the predicate, so the tenant
// column a statement SETS went unchecked: a Save whose struct left the tenant blank wrote
// the empty string over the row's tenant, and one that named another tenant (or an Updates
// with a struct or a map doing so) moved the row there. This brings the update path to the
// same rule as the create path, under a tenant context:
//
//   - a tenant column being written with a blank value is stamped with the context's tenant;
//   - one naming a different tenant is refused with ErrTenantMismatch;
//   - a map that sets the tenant column (by column or field name) follows the same rule,
//     and a value that is not a plain string cannot be checked and is refused.
//
// A system context stays exempt, as everywhere else in this file's callbacks.

// tenantScopeUpdate injects the tenant predicate, then applies the rule above to what the
// statement sets.
func tenantScopeUpdate(db *gorm.DB) {
	field, tenant, ok := scopedTenant(db)
	if !ok {
		return
	}
	addTenantPredicate(db, field, tenant)
	if err := checkUpdatedTenant(db, field, tenant); err != nil {
		_ = db.AddError(err)
	}
}

func checkUpdatedTenant(db *gorm.DB, field *schema.Field, tenant string) error {
	mismatch := func(named string) error {
		return fmt.Errorf("%w: an update sets the tenant column to %q while the context names %q",
			ErrTenantMismatch, named, tenant)
	}
	dest := db.Statement.Dest
	if m, ok := dest.(map[string]interface{}); ok {
		for _, key := range []string{field.DBName, field.Name} {
			v, present := m[key]
			if !present {
				continue
			}
			named, isString := v.(string)
			switch {
			case !isString:
				return mismatch(fmt.Sprintf("%v", v))
			case named == "":
				m[key] = tenant
			case named != tenant:
				return mismatch(named)
			}
		}
		return nil
	}

	v := reflect.ValueOf(dest)
	for v.IsValid() && v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(field.Name)
	if !f.IsValid() || f.Kind() != reflect.String {
		return nil
	}
	switch named := f.String(); {
	case named == tenant:
		return nil
	case named != "":
		return mismatch(named)
	case !tenantColumnIsWritten(db, field):
		return nil // a zero field is skipped by Updates, so nothing blank reaches the row
	}
	if !v.CanAddr() {
		// Updates(T{...}) hands gorm a value; stamp a copy, as Statement.SetColumn does.
		cp := reflect.New(v.Type())
		cp.Elem().Set(v)
		db.Statement.Dest = cp.Interface()
		v = cp.Elem()
		f = v.FieldByName(field.Name)
	}
	f.SetString(tenant)
	return nil
}

// tenantColumnIsWritten reports whether a blank tenant would be written: Save selects every
// column, and a caller may select the tenant column by either name.
func tenantColumnIsWritten(db *gorm.DB, field *schema.Field) bool {
	for _, s := range db.Statement.Selects {
		if s == "*" || s == field.Name || s == field.DBName {
			return true
		}
	}
	return false
}

// addTenantPredicate is the predicate every scoped statement carries.
func addTenantPredicate(db *gorm.DB, field *schema.Field, tenant string) {
	db.Statement.AddClause(clause.Where{
		Exprs: []clause.Expression{
			clause.Eq{
				// The column comes from the schema, not from a literal. A literal
				// "tenant_id" here silently produced NO predicate at all for a model
				// spelling it "tenant" — the statement would have referenced a column
				// that does not exist on the table.
				Column: clause.Column{Table: db.Statement.Table, Name: field.DBName},
				Value:  tenant,
			},
		},
	})
}
