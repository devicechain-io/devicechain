// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// Audit categories distinguish a row captured from an entity mutation (the GORM
// callbacks) from one recorded for an authentication event (the login/refresh
// path), which is not a mutation. Auth-event rows leave TableName/EntityPK empty.
const (
	AuditCategoryMutation = "mutation"
	AuditCategoryAuth     = "auth"
)

// Audit operations for authentication events (ADR-019). Mutation operations are
// the literal "create"/"update"/"delete" recorded by the callbacks.
const (
	AuditOpLogin       = "login"
	AuditOpLoginFailed = "login_failed"
	AuditOpRefresh     = "refresh"
)

// AuditEvent is one row of the tenant-scoped audit journal (ADR-019): a record,
// emitted by construction on every entity mutation, of who changed which entity
// when. It is metadata-only — the changed column values are deliberately NOT
// stored, so the journal carries no PII or credential material (a future
// enhancement could add a redacted value diff).
//
// It carries a TenantId field (not the TenantScoped embed, but matched the same
// way by the scoping callbacks), so a read of the journal is tenant-scoped like
// any other table, while a write stamps the acting tenant.
type AuditEvent struct {
	ID           uint      `gorm:"primaryKey"`
	OccurredTime time.Time `gorm:"not null;index:idx_audit_tenant_time,priority:2,sort:desc"`
	TenantId     string    `gorm:"index:idx_audit_tenant_time,priority:1"`
	// Category is "mutation" (an entity create/update/delete) or "auth" (a login,
	// failed login, or refresh). It discriminates the two row shapes.
	Category string `gorm:"not null"`
	// Actor is the JWT subject that performed the mutation, "system" for a
	// deliberate system-context operation (bootstrap, the ingest pipeline), or ""
	// when neither is present. For an auth event it is the subject username.
	Actor string
	// TableName is the mutated table (schema-qualified as the dialect renders it);
	// empty for an auth event.
	TableName string
	// Operation is one of "create"/"update"/"delete" for a mutation, or
	// "login"/"login_failed"/"refresh" for an auth event.
	Operation string `gorm:"not null"`
	// EntityPK is the primary key of the affected row when the statement targets a
	// single keyed row; empty for a bulk/condition update or delete (RowsAffected
	// then conveys the scope).
	EntityPK string
	// EntityLabel is a human-facing, non-sensitive identifier of the affected row
	// (e.g. a token, name, or email), recorded when the mutated model implements
	// AuditLabeler and its label is set. Empty otherwise (a model that opts out, a
	// bulk statement, or a delete-by-key that carried no populated struct). It is
	// display sugar over (TableName, EntityPK) — deliberately not a value dump, so
	// the metadata-only contract of the journal (ADR-019) holds.
	EntityLabel  string
	RowsAffected int64
}

// auditSchemaName is the gorm schema (struct) name used by the recursion guard so
// the journal's own inserts do not generate further audit rows.
const auditSchemaName = "AuditEvent"

// AuditExempt is implemented by a model to opt OUT of the audit journal. The
// high-volume, append-only data-plane tables — telemetry events and the
// device-state projection — implement it: they are immutable facts and derived
// state, not the control-plane entity mutations the journal exists to record
// (ADR-019), and auditing them would multiply write volume for no security value.
// The opt-out is explicit so a newly added control-plane entity is audited by
// default; a forgotten opt-IN would be an unrecorded gap, which is exactly what
// "capture by construction" exists to prevent.
type AuditExempt interface {
	AuditExempt() bool
}

// AuditLabeler is implemented by a model to contribute a human-facing,
// non-sensitive label for the affected row to its audit entry (e.g. a token,
// name, or email) — turning an opaque "iam_roles #3" into "iam_roles operator"
// in an audit view. The label must never be a secret or free-form value dump;
// the journal stays metadata-only by design (ADR-019), and the model chooses
// exactly which safe identifier to expose. Opting in is optional: a model that
// does not implement it simply records no label and the reader falls back to the
// (table, pk) reference.
type AuditLabeler interface {
	AuditLabel() string
}

// auditLabel returns the mutated row's label when the destination is a single
// struct that implements AuditLabeler and the label is set. It reads the actual
// mutated instance (db.Statement.ReflectValue) — not a fresh zero value like the
// AuditExempt check — because the label is per-row data, not a constant.
func auditLabel(db *gorm.DB) string {
	rv := db.Statement.ReflectValue
	if rv.Kind() != reflect.Struct {
		return ""
	}
	if labeler, ok := rv.Interface().(AuditLabeler); ok {
		return labeler.AuditLabel()
	}
	if rv.CanAddr() {
		if labeler, ok := rv.Addr().Interface().(AuditLabeler); ok {
			return labeler.AuditLabel()
		}
	}
	return ""
}

// isAuditExempt reports whether the mutated model opts out of the journal.
func isAuditExempt(db *gorm.DB) bool {
	if db.Statement.Schema == nil {
		return false
	}
	mt := db.Statement.Schema.ModelType
	if mt == nil || mt.Kind() != reflect.Struct {
		return false
	}
	if exempt, ok := reflect.New(mt).Interface().(AuditExempt); ok {
		return exempt.AuditExempt()
	}
	return false
}

// RegisterAuditJournal installs global GORM After callbacks that append an
// AuditEvent for every create/update/delete by construction (ADR-019), alongside
// the tenant-scope callbacks. The audit row is written inside the mutation's
// transaction (a fresh session sharing the same connection), so it commits iff
// the mutation commits; a failure to record the audit row fails the mutation
// (fail-closed — no unrecorded mutation).
func RegisterAuditJournal(db *gorm.DB) error {
	for _, register := range []func() error{
		func() error {
			return db.Callback().Create().After("gorm:create").Register("dc:audit_create", auditCallback("create"))
		},
		func() error {
			return db.Callback().Update().After("gorm:update").Register("dc:audit_update", auditCallback("update"))
		},
		func() error {
			return db.Callback().Delete().After("gorm:delete").Register("dc:audit_delete", auditCallback("delete"))
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// auditCallback builds the After-callback for one operation.
func auditCallback(operation string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		// Nothing to record if the statement already failed.
		if db.Error != nil {
			return
		}
		// Recursion guard: the journal's own inserts must not generate audit rows.
		if db.Statement.Schema != nil && db.Statement.Schema.Name == auditSchemaName {
			return
		}
		// High-volume data-plane tables opt out (telemetry, device-state projection).
		if isAuditExempt(db) {
			return
		}
		// A statement that mutated no rows (e.g. a no-op update) is not audited.
		if db.Statement.RowsAffected == 0 {
			return
		}

		row := &AuditEvent{
			OccurredTime: time.Now().UTC(),
			TenantId:     auditTenant(db),
			Category:     AuditCategoryMutation,
			Actor:        auditActor(db),
			TableName:    db.Statement.Table,
			Operation:    operation,
			EntityPK:     auditPrimaryKey(db),
			EntityLabel:  auditLabel(db),
			RowsAffected: db.Statement.RowsAffected,
		}

		// Write the audit row on a fresh session that shares this statement's
		// connection (and thus its transaction), carrying the same context so the
		// scoping callbacks see the same tenant/system marker. A failure rolls the
		// whole mutation back.
		session := db.Session(&gorm.Session{NewDB: true, Context: db.Statement.Context})
		if err := session.Create(row).Error; err != nil {
			_ = db.AddError(err)
		}
	}
}

// auditActor resolves the acting identity for the mutation from the statement
// context: the JWT subject when authenticated, "system" for a deliberate
// system-context operation, else "".
func auditActor(db *gorm.DB) string {
	ctx := db.Statement.Context
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		return claims.Username
	}
	if core.IsSystemContext(ctx) {
		return "system"
	}
	return ""
}

// auditTenant returns the acting tenant from context, or "" when none is present
// (a system-context or non-tenant operation). The tenant-scope create callback
// also stamps this on the audit row; resolving it here keeps the row correct even
// on the system-context path that callback intentionally skips.
func auditTenant(db *gorm.DB) string {
	if tenant, ok := core.TenantFromContext(db.Statement.Context); ok {
		return tenant
	}
	return ""
}

// auditPrimaryKey returns a string form of the mutated row's primary key when the
// statement targets a single struct whose key is set; it returns "" for a batch
// (slice) destination or a condition-only update/delete, where no single key
// identifies the affected rows.
func auditPrimaryKey(db *gorm.DB) string {
	if db.Statement.Schema == nil {
		return ""
	}
	field := db.Statement.Schema.PrioritizedPrimaryField
	if field == nil {
		return ""
	}
	rv := db.Statement.ReflectValue
	if rv.Kind() != reflect.Struct {
		return ""
	}
	value, isZero := field.ValueOf(db.Statement.Context, rv)
	if isZero {
		return ""
	}
	return fmt.Sprint(value)
}

// RecordAuthEvent appends an audit row for an authentication event — a login,
// failed login, or refresh (ADR-019). These are not GORM entity mutations, so
// they are not captured by the mutation callbacks and are written here directly.
//
// It runs under a system context so the tenant-scope callback does not require a
// tenant (a failed login for an unknown user has none); the tenant is set
// explicitly and may be "". Unlike the fail-closed mutation path, recording is
// best-effort: the caller logs but does not fail interactive authentication on an
// audit-write error, since failing every login when the audit table blips would
// be a worse outcome (and a denial-of-service lever) than a missed row.
func (rdb *RdbManager) RecordAuthEvent(ctx context.Context, operation, actor, tenant string) error {
	row := &AuditEvent{
		OccurredTime: time.Now().UTC(),
		TenantId:     tenant,
		Category:     AuditCategoryAuth,
		Actor:        actor,
		Operation:    operation,
	}
	return rdb.DB(core.WithSystemContext(ctx)).Create(row).Error
}
