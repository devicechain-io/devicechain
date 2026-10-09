// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The tenant predicate this package injects reaches SELECT, UPDATE and DELETE through the
// statement's WHERE clause. An upsert has a second place a write can happen: the DO UPDATE
// arm of INSERT ... ON CONFLICT, which is not part of the WHERE clause and which a
// conflicting row reaches without ever being matched by it. gorm's Save is an upsert too:
// when its scoped UPDATE matches nothing it falls back to an INSERT ... ON CONFLICT
// (primary key) DO UPDATE of every column. This file closes that arm, as defense in depth;
// no call site in the tree is known to need it.
//
//   - Every upsert on a tenant-scoped table has its update arm limited to rows of the same
//     tenant (tenantUpsertGuard). A conflicting row of another tenant is left exactly as it
//     was: nothing is overwritten and the row's tenant is never rewritten.
//   - An upsert whose explicit conflict target does not name the tenant column is refused
//     with ErrTenantlessUpsert unless the call site is marked reviewed (AllowTenantlessUpsert).
//     Such a key is unique across tenants, so a collision between two tenants is not a
//     same-tenant update, and the caller should have said why that cannot happen.
//   - An upsert that, with the guard as its only condition, wrote fewer rows than it was
//     given fails with ErrTenantUpsertConflict rather than reporting success.

// ErrTenantlessUpsert is the refusal for an INSERT ... ON CONFLICT ... DO UPDATE on a
// tenant-scoped table whose conflict target does not include the tenant column (or is a
// named constraint, which cannot be inspected) and whose call site is not marked reviewed.
var ErrTenantlessUpsert = errors.New("tenant isolation refused the upsert: its ON CONFLICT target does not " +
	"include the tenant column, so a row of another tenant could collide with it. Add the tenant column to " +
	"the conflict target or, if the key cannot collide across tenants, mark the call site with " +
	"rdb.AllowTenantlessUpsert(db, reason) and say why in the reason")

// ErrTenantUpsertConflict is the failure for an upsert on a tenant-scoped table that wrote
// fewer rows than it was given because a conflicting row belongs to another tenant. Nothing
// of that row was changed; the error exists so the caller is told, not left with a silent no-op.
var ErrTenantUpsertConflict = errors.New("tenant isolation refused the upsert: a row being written conflicts " +
	"with a row of another tenant, which was left unchanged")

// tenantlessUpsertKey is the statement setting AllowTenantlessUpsert writes.
const tenantlessUpsertKey = "dc:tenantless_upsert_reviewed"

// AllowTenantlessUpsert marks db's next statement as an upsert whose ON CONFLICT target
// deliberately omits the tenant column, and records why that is safe. The reason is
// required: a marker with an empty reason is ignored, so the upsert is still refused. The
// marker does not switch the tenant guard off; the update arm is still limited to rows of
// the statement's own tenant.
func AllowTenantlessUpsert(db *gorm.DB, reason string) *gorm.DB {
	return db.Set(tenantlessUpsertKey, strings.TrimSpace(reason))
}

func tenantlessUpsertReviewed(db *gorm.DB) bool {
	v, ok := db.Get(tenantlessUpsertKey)
	reason, _ := v.(string)
	return ok && reason != ""
}

// tenantUpsertGuard is `<table>.<tenant col> = excluded.<tenant col>`: the existing row's
// tenant equals the incoming row's, which the create callback has just stamped with the
// context's tenant. A named type, not a clause.Expr, so a statement the callback has already
// guarded can be recognised and is not guarded twice.
type tenantUpsertGuard struct {
	table, column string
}

func (g tenantUpsertGuard) Build(b clause.Builder) {
	b.WriteQuoted(clause.Column{Table: g.table, Name: g.column})
	b.WriteString(" = ")
	b.WriteQuoted(clause.Column{Table: "excluded", Name: g.column})
}

// parenthesised writes a caller's whole Where inside parentheses. gorm joins a clause's
// expressions with a bare AND and adds no parentheses of its own, so an expression of the
// caller's that contains an OR (or a leading Or) would otherwise take the tenant guard as an
// operand of that OR instead of a condition on the whole.
type parenthesised struct {
	inner clause.Where
}

func (p parenthesised) Build(b clause.Builder) {
	b.WriteByte('(')
	p.inner.Build(b)
	b.WriteByte(')')
}

// upsertClause returns the statement's ON CONFLICT clause when it is a DO UPDATE one.
func upsertClause(db *gorm.DB) (clause.OnConflict, bool) {
	c, ok := db.Statement.Clauses["ON CONFLICT"]
	if !ok {
		return clause.OnConflict{}, false
	}
	oc, ok := c.Expression.(clause.OnConflict)
	if !ok || oc.DoNothing || (!oc.UpdateAll && len(oc.DoUpdates) == 0) {
		return oc, false
	}
	return oc, true
}

// targetNamesTenant reports whether an explicit conflict target includes column. An empty
// target is the primary key (gorm fills it in from the schema), which Save relies on, so it
// is accepted here; the guard confines it and ErrTenantUpsertConflict reports a collision.
// A named constraint cannot be inspected and so does not count.
func targetNamesTenant(oc clause.OnConflict, column string) bool {
	if oc.OnConstraint != "" {
		return false
	}
	if len(oc.Columns) == 0 {
		return true
	}
	for _, c := range oc.Columns {
		if strings.EqualFold(c.Name, column) {
			return true
		}
	}
	return false
}

// guardTenantUpsert limits an upsert's update arm to the statement's own tenant and refuses
// an unreviewed tenant-less conflict target. column is the model's tenant column.
func guardTenantUpsert(db *gorm.DB, column string) {
	oc, ok := upsertClause(db)
	if !ok {
		return
	}
	if !targetNamesTenant(oc, column) && !tenantlessUpsertReviewed(db) {
		_ = db.AddError(fmt.Errorf("%w (table %q)", ErrTenantlessUpsert, db.Statement.Table))
		return
	}
	guard := tenantUpsertGuard{table: db.Statement.Table, column: column}
	if len(oc.Where.Exprs) > 0 && oc.Where.Exprs[0] == clause.Expression(guard) {
		return // already guarded: a statement reused for several chunks is built more than once
	}
	if err := refuseTenantReassignment(oc, column); err != nil {
		_ = db.AddError(fmt.Errorf("%w (table %q)", err, db.Statement.Table))
		return
	}
	// A fresh slice: the caller's Where is often a package-level value shared by every
	// statement, and appending to it in place would be a data race. The guard comes first
	// and stands alone; the caller's own condition follows, parenthesised.
	exprs := []clause.Expression{guard}
	if len(oc.Where.Exprs) > 0 {
		exprs = append(exprs, parenthesised{inner: oc.Where})
	}
	oc.Where = clause.Where{Exprs: exprs}
	db.Statement.AddClause(oc)
}

// refuseTenantReassignment rejects an update arm that assigns the tenant column to anything
// but the incoming row's own value (excluded.<column>), which would move a row between tenants.
func refuseTenantReassignment(oc clause.OnConflict, column string) error {
	for _, a := range oc.DoUpdates {
		if !strings.EqualFold(a.Column.Name, column) {
			continue
		}
		if v, ok := a.Value.(clause.Column); ok && v.Table == "excluded" && strings.EqualFold(v.Name, column) {
			continue
		}
		return ErrTenantUpsertReassign
	}
	return nil
}

// tenantUpsertCheck turns a guarded upsert that wrote fewer rows than it was given into an
// error. It runs inside the statement's transaction, so the error rolls the statement back.
// Under SkipDefaultTransaction with no caller transaction there is none to roll back: the
// rows of a colliding batch that did not collide stay committed.
//
// It only speaks when the guard is the clause's sole condition: a DO UPDATE with a condition
// of its own (a monotonic-clock guard, say) legitimately writes fewer rows than it is given,
// and a DO NOTHING outcome, which gorm substitutes when a model has no non-key column to
// update, cannot be told from a collision.
func tenantUpsertCheck(db *gorm.DB) {
	if db.Error != nil || db.DryRun {
		return
	}
	oc, ok := upsertClause(db)
	if !ok || len(oc.Where.Exprs) == 0 {
		return
	}
	if len(oc.Where.Exprs) != 1 {
		return
	}
	if _, guarded := oc.Where.Exprs[0].(tenantUpsertGuard); !guarded {
		return
	}
	if want := createRowCount(db.Statement.Dest); want > 0 && db.RowsAffected < int64(want) {
		_ = db.AddError(fmt.Errorf("%w (table %q: %d of %d rows written)",
			ErrTenantUpsertConflict, db.Statement.Table, db.RowsAffected, want))
	}
}

// createRowCount is how many rows a create destination holds; zero when it cannot say.
func createRowCount(dest any) int {
	v := reflect.ValueOf(dest)
	for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return 0
	}
	switch v.Kind() {
	case reflect.Struct, reflect.Map:
		return 1
	case reflect.Slice, reflect.Array:
		return v.Len()
	}
	return 0
}

// ErrTenantUpsertReassign is the refusal for an upsert whose update arm assigns the tenant
// column to anything but the incoming row's own value.
var ErrTenantUpsertReassign = errors.New("tenant isolation refused the upsert: its update arm assigns the " +
	"tenant column to something other than the incoming row's tenant")
