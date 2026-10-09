// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"
	"strings"

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
//   - a map key or SET clause that names the tenant column, in any spelling gorm resolves,
//     follows the same rule, and a value that is not a plain string cannot be checked and is
//     refused; so is a destination that holds the column as anything but a string.
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

// checkUpdatedTenant looks at every way a statement can set the tenant column and applies the
// rule to each. It asks gorm the questions gorm itself asks of the same statement: which
// column a map key or SET column names (Schema.LookUpField, with a raw column's quotes
// stripped, since gorm passes a name it cannot resolve through as a raw column), which
// columns are written (Statement.SelectAndOmitColumns), and which field of the destination
// is the column (the destination's own schema), rather than spelling the possibilities out.
//
// Stamping a blank writes into the caller's map or struct, the way the create path writes the
// tenant onto the caller's rows; a destination passed by value is stamped on a copy.
func checkUpdatedTenant(db *gorm.DB, field *schema.Field, tenant string) error {
	if err := checkSetClause(db, field, tenant); err != nil {
		return err
	}
	if m, ok := db.Statement.Dest.(map[string]interface{}); ok {
		for k, v := range m {
			if !isTenantColumnName(db, field, k) {
				continue
			}
			stamp, err := resolveTenantValue(v, tenant)
			if err != nil {
				return err
			}
			if stamp {
				m[k] = tenant
			}
		}
		return nil
	}

	v := reflect.ValueOf(db.Statement.Dest)
	for v.IsValid() && v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil
	}
	// The destination may be a different type from the model, so the column is looked up in
	// its own schema, exactly as gorm does when it builds the SET list.
	dest := &gorm.Statement{DB: db}
	if err := dest.Parse(db.Statement.Dest); err != nil || dest.Schema == nil {
		return nil
	}
	f := dest.Schema.LookUpField(field.DBName)
	if f == nil {
		return nil
	}
	if f.FieldType.Kind() != reflect.String {
		return fmt.Errorf("%w: the update destination holds the tenant column as %s, which cannot be checked "+
			"against the context's tenant; use a plain string field", ErrTenantMismatch, f.FieldType)
	}
	named := f.ReflectValueOf(db.Statement.Context, v).String()
	switch {
	case named == tenant:
		return nil
	case named != "":
		return updatedTenantMismatch(named, tenant)
	case !tenantColumnIsWritten(db, field):
		return nil // a zero field is skipped by Updates, so nothing blank reaches the row
	}
	if !v.CanAddr() {
		// Updates(T{...}) hands gorm a value; stamp a copy, as Statement.SetColumn does.
		cp := reflect.New(v.Type())
		cp.Elem().Set(v)
		db.Statement.Dest = cp.Interface()
		v = cp.Elem()
	}
	return f.Set(db.Statement.Context, v, tenant)
}

// checkSetClause covers a SET handed to the statement as a clause (Clauses(clause.Set{...}),
// clause.Assignments): gorm builds no assignments of its own when one is present.
func checkSetClause(db *gorm.DB, field *schema.Field, tenant string) error {
	c, ok := db.Statement.Clauses["SET"]
	if !ok {
		return nil
	}
	set, ok := c.Expression.(clause.Set)
	if !ok {
		return nil
	}
	var stamped clause.Set
	for i, a := range set {
		if !isTenantColumnName(db, field, a.Column.Name) {
			continue
		}
		stamp, err := resolveTenantValue(a.Value, tenant)
		if err != nil {
			return err
		}
		if stamp {
			if stamped == nil {
				stamped = append(clause.Set(nil), set...)
			}
			stamped[i].Value = tenant
		}
	}
	if stamped != nil {
		db.Statement.AddClause(stamped)
	}
	return nil
}

// resolveTenantValue applies the rule to one value being assigned to the tenant column: it
// reports whether a blank is to be replaced by the context's tenant, and refuses a different
// tenant and any value that is not a plain string (an expression, a nil, a pointer).
func resolveTenantValue(v interface{}, tenant string) (stamp bool, err error) {
	named, isString := v.(string)
	switch {
	case !isString:
		return false, updatedTenantMismatch(fmt.Sprintf("%v", v), tenant)
	case named == "":
		return true, nil
	case named != tenant:
		return false, updatedTenantMismatch(named, tenant)
	}
	return false, nil
}

func updatedTenantMismatch(named, tenant string) error {
	return fmt.Errorf("%w: an update sets the tenant column to %q while the context names %q",
		ErrTenantMismatch, named, tenant)
}

// isTenantColumnName reports whether name, as a map key or a SET column, is the tenant
// column: either gorm resolves it to that field, or, as a name gorm would pass through as a
// raw column, it is the column once its quoting and any table prefix are removed.
func isTenantColumnName(db *gorm.DB, field *schema.Field, name string) bool {
	if db.Statement.Schema != nil && db.Statement.Schema.LookUpField(name) == field {
		return true
	}
	n := strings.Trim(name, "\"`[] ")
	if i := strings.LastIndex(n, "."); i >= 0 {
		n = strings.Trim(n[i+1:], "\"`[] ")
	}
	return strings.EqualFold(n, field.DBName) || strings.EqualFold(n, field.Name)
}

// tenantColumnIsWritten reports whether a blank tenant would be written, by asking gorm which
// columns the statement writes: Save selects everything, and Select may name the column in
// any of the forms gorm resolves.
func tenantColumnIsWritten(db *gorm.DB, field *schema.Field) bool {
	selected, _ := db.Statement.SelectAndOmitColumns(false, true)
	if selected[field.DBName] {
		return true
	}
	for name, on := range selected {
		if on && isTenantColumnName(db, field, name) {
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
