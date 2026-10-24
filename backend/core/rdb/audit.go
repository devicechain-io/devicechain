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
	AuditOpLogout      = "logout"
	// AuditOpIdentityLinked and AuditOpIdentityUnlinked record an external-identity-provider
	// account being bound to, or removed from, an identity. The actor is the administrator
	// (or, for a self-service link, the identity itself).
	AuditOpIdentityLinked   = "identity_linked"
	AuditOpIdentityUnlinked = "identity_unlinked"
	// AuditOpSsoLogin and AuditOpSsoRefused record a single sign-on completing, and one
	// being refused after the provider vouched for the person. The actor is the email the
	// provider asserted; no token, code or claim value is ever recorded.
	AuditOpSsoLogin   = "sso_login"
	AuditOpSsoRefused = "sso_refused"
	// AuditOpIdpMfaAcknowledged records an administrator creating an instance connection
	// on their own assurance that the provider enforces multi-factor sign-in.
	AuditOpIdpMfaAcknowledged = "idp_mfa_acknowledged"
	// AuditOpIdpCaBundleChanged records an administrator setting or clearing the private CA
	// bundle an identity provider connection trusts. The label is the connection token; the
	// certificates themselves are not recorded.
	AuditOpIdpCaBundleChanged = "idp_ca_bundle_changed"
	// AuditOpIdentityProvisioned records a first single sign-on creating the identity it
	// signed in as (just-in-time provisioning). The actor is the email the provider asserted.
	AuditOpIdentityProvisioned = "identity_provisioned"
	// AuditOpRoleMappingChanged records an administrator replacing a connection's group
	// mappings for a tenant, or its just-in-time provisioning settings. The actor is the
	// administrator and the tenant is the one the change applies to, when there is one.
	AuditOpRoleMappingChanged = "role_mapping_changed"
	// AuditOpSsoBreakGlass records a superuser, signed in through the operator's identity
	// provider, entering a tenant. The tenant is the one entered and the label names the
	// connection.
	AuditOpSsoBreakGlass = "sso_break_glass"
	// AuditOpSsoEnforcementBypassed records a superuser with a PASSWORD session entering a
	// tenant that requires single sign-on, the one exemption from that requirement. The
	// tenant is the one entered.
	AuditOpSsoEnforcementBypassed = "sso_enforcement_bypassed"
	// AuditOpSessionsRevoked records an administrator ending every session an identity
	// has. The label names the identity.
	AuditOpSessionsRevoked = "sessions_revoked"
	// AuditOpPasswordCleared records an administrator removing an identity's password,
	// leaving it able to sign in only through single sign-on. The label names the identity.
	AuditOpPasswordCleared = "password_cleared"
	// AuditOpServiceAccountToken records a tenant's service account obtaining an access
	// token (the OAuth client_credentials grant). The actor is the account's subject
	// (auth.ServiceAccountSubject), the tenant its tenant. A refused attempt writes no row,
	// so a spray of wrong secrets cannot drive database writes.
	AuditOpServiceAccountToken = "service_account_token"
)

// AuditEvent is one row of the tenant-scoped audit journal (ADR-019): a record,
// emitted by construction on every entity mutation, of who changed which entity
// when. It is metadata-only — the changed column values are deliberately NOT
// stored, so no credential or payload material reaches it (a future enhancement
// could add a redacted value diff).
//
// 🔴 IT DOES CARRY PERSONAL DATA, AND THIS COMMENT USED TO SAY IT DID NOT. The
// claim was "no PII or credential material", written eight lines above a field
// documented as holding "a token, name, or email". Two columns name people:
// Actor is the acting JWT's username, which for every human token IS the email
// (core/auth/issuer.go mints username and email from the same value), and
// EntityLabel is an identity row's email or a customer-chosen token, which is
// routinely a person's or a company's name. Metadata-only bounds what a mutation
// can leak INTO the journal; it says nothing about who the journal names.
//
// That is what makes the ADR-077 retention a decision rather than a formality.
// The journal is kept through a tenant purge, because sweeping it destroys the
// evidence of the erasure it is part of — and both columns above are emptied for
// the purged tenant, irreversibly, by the redaction registered in
// core/tenantpurge. Written empty rather than hashed: an email is a low-entropy
// enumerable space, so a digest of one is reversible by anyone with a user list.
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
	// EntityLabel is a human-facing identifier of the affected row (e.g. a token,
	// name, or email), recorded when the mutated model implements AuditLabeler and
	// its label is set. Empty otherwise (a model that opts out, a bulk statement, or
	// a delete-by-key that carried no populated struct). It is display sugar over
	// (TableName, EntityPK) — deliberately not a value dump, so the metadata-only
	// contract of the journal (ADR-019) holds.
	//
	// 🔴 IT IS NOT "NON-SENSITIVE", WHICH IS WHAT THIS USED TO SAY. Not being a
	// credential and not being personal data are different properties, and the
	// parenthetical above already admits the second: an identity row's label is its
	// email, and the customer-chosen tokens promoted from TokenReference across some
	// two dozen models are routinely people's and companies' names. It is one of the
	// two columns emptied for a purged tenant — see the type doc.
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

// AuditLabeler is implemented by a model to contribute a human-facing label for
// the affected row to its audit entry (e.g. a token, name, or email) — turning an
// opaque "iam_roles #3" into "iam_roles operator" in an audit view. The label must
// never be a secret or free-form value dump; the journal stays metadata-only by
// design (ADR-019), and the model chooses exactly which safe identifier to expose.
//
// 🔴 "SAFE" HERE MEANS "NOT A CREDENTIAL", NOT "NOT PERSONAL". A label is expected
// to identify — that is its whole job — and several of the implementations return an
// email or a customer-chosen name. What that costs is paid at erasure, not here: the
// column is emptied for a purged tenant (see AuditEvent). Opting in is optional: a model that
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

// RegisterAuditJournal installs global GORM callbacks that append an AuditEvent for
// every create/update/delete by construction (ADR-019), alongside the tenant-scope
// callbacks.
//
// 🔴 THE AUDIT ROW COMMITS WITH THE MUTATION, OR NEITHER DOES. Each callback is placed
// After the statement and Before("gorm:commit_or_rollback_transaction"), so it runs
// inside the statement's own implicit transaction (or inside the transaction the caller
// opened, which it joins). A failed audit write is added to the statement's error, the
// commit callback sees it and rolls the mutation back, and the caller is told the
// truth: the change did NOT happen. The previous registration was a bare
// After("gorm:create"), which gorm sorts AFTER the commit callback; the mutation was
// already durable when the journal row was written, so an audit failure reported
// failure for a change that had been saved and left it unjournaled.
//
// What is still best effort, deliberately: authentication events (RecordAuthEvent),
// which are not mutations and must not turn an audit-table blip into a sign-in outage.
// What is not journaled at all: raw SQL (Exec/Raw), which never reaches these processors.
// And a session opened with SkipDefaultTransaction and no caller transaction has no
// transaction to share, so there the audit row is a second, separate write.
//
// Lock order: the journal insert is the LAST statement of the transaction it joins, so
// a transaction takes its entity locks in the order it always did and the journal's
// rows (a plain append with a serial key and no unique index, so no row is contended
// between writers) afterwards. The journal takes no lock another writer waits for.
func RegisterAuditJournal(db *gorm.DB) error {
	for _, register := range []func() error{
		func() error {
			return db.Callback().Create().After("gorm:create").Before("gorm:commit_or_rollback_transaction").
				Register("dc:audit_create", auditCallback("create"))
		},
		func() error {
			return db.Callback().Update().After("gorm:update").Before("gorm:commit_or_rollback_transaction").
				Register("dc:audit_update", auditCallback("update"))
		},
		func() error {
			return db.Callback().Delete().After("gorm:delete").Before("gorm:commit_or_rollback_transaction").
				Register("dc:audit_delete", auditCallback("delete"))
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
		// scoping callbacks see the same tenant/system marker. A failure is added to
		// this statement's error, so the commit callback rolls the mutation back with it.
		session := db.Session(&gorm.Session{NewDB: true, Context: db.Statement.Context})
		if err := session.Create(row).Error; err != nil {
			_ = db.AddError(err)
		}
	}
}

// auditActorKey is the context key WithAuditActor stores its actor under.
type auditActorKey struct{}

// WithAuditActor names the actor the audit journal records for mutations made under the
// returned context, for a write that has no user behind it: a device registering itself
// through a provisioning profile has no JWT, so without a name its rows would carry no
// actor at all. The name is the caller's to compose ("provisioning-profile:<token>"), and
// must come from the server's own decision about who authenticated, never from request
// input.
//
// Claims win: a context that carries an authenticated identity is journalled as that
// identity whatever this says, so naming an actor can never relabel a user's write. An
// empty actor leaves the context unchanged.
func WithAuditActor(ctx context.Context, actor string) context.Context {
	if actor == "" {
		return ctx
	}
	return context.WithValue(ctx, auditActorKey{}, actor)
}

// auditActor resolves the acting identity for the mutation from the statement
// context: the JWT subject when authenticated, else the actor WithAuditActor named, "system"
// for a deliberate system-context operation, else "".
func auditActor(db *gorm.DB) string {
	ctx := db.Statement.Context
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		return claims.Username
	}
	if actor, ok := ctx.Value(auditActorKey{}).(string); ok {
		return actor
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
	return rdb.RecordAuthEventLabeled(ctx, operation, actor, tenant, "")
}

// RecordAuthEventLabeled is RecordAuthEvent for an event that concerns something besides
// the actor and the tenant, naming it in the row's EntityLabel (a connection's token, the
// identity an administrator acted on). The label is a name, never a credential.
func (rdb *RdbManager) RecordAuthEventLabeled(ctx context.Context, operation, actor, tenant, label string) error {
	row := &AuditEvent{
		OccurredTime: time.Now().UTC(),
		TenantId:     tenant,
		Category:     AuditCategoryAuth,
		Actor:        actor,
		Operation:    operation,
		EntityLabel:  label,
	}
	return rdb.DB(core.WithSystemContext(ctx)).Create(row).Error
}
