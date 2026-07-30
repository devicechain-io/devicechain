// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in model.go.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model
// gains a column, the column arrives in a NEW migration appended after the baseline — these
// stay exactly as they are. They are wrong about the current schema by design, and that is
// what makes them correct about this migration.
//
// They are self-contained down to the mixins: gorm.Model and rdb.TenantScoped are INLINED
// rather than embedded, so a change in core cannot silently rewrite this migration from a
// PR that never touched this service.
//
// 🔴 FIELD ORDER IS NOT COSMETIC HERE. The chain built these tables incrementally, and
// `ALTER TABLE ... ADD COLUMN` appends, so the surviving physical column order is the order
// the migrations ran in — device_states carries presence_source/session_id/presence_time
// (ADR-067 S2), then external_id, then source, at the END, after inactivity_timeout, not
// grouped with the other presence-ish columns where a fresh reading would put them.
// AutoMigrate emits columns in struct-field order, so reordering these fields changes the
// schema. migration-diff reports that as a reordering rather than letting it pass.
//
// 🔴 NEITHER TYPE HAS A TableName METHOD. core/rdb pins the gorm NamingStrategy's
// TablePrefix to the functional area, and that prefix applies only when gorm DERIVES a
// table name; an explicit TableName bypasses it and every index on the table is silently
// renamed (idx_device-state_device_states_deleted_at → idx_device_states_deleted_at). Gorm
// derives "device_states" and "latest_measurements" from the type names, so RENAMING A TYPE
// RETARGETS THE MIGRATION. Leave both the absence and the names alone.
//
// 🔑 WHY INLINED — stated carefully, because the obvious version of this argument is BACKWARDS.
// An earlier version of this comment claimed the golden gate now "largely catches" a core mixin
// edit, making the inlining rule belt-and-braces. That is false, and measurably so: nothing in
// this file is reachable from backend/core, so editing rdb.TenantScoped cannot change one byte of
// what this migration emits — there is no divergence for any gate to catch. INLINING IS WHAT
// REMOVES THE MIXIN FROM THE GATE'S FIELD OF VIEW. Crediting the gate for that coverage inverts
// cause and effect.
//
// The contrast is the useful part, and it is deliberate: core/secrets/migration.go DOES embed
// gorm.Model and rdb.TenantScoped, because the secret-store schema genuinely IS core's and a
// change to it SHOULD change all three consumers. Measured — adding a field to rdb.TenantScoped
// reddens migration-diff verify for exactly the three areas carrying a secret store, and no
// others. So the repo runs both patterns on purpose; the test is whose schema it is.
//
// What inlining therefore buys is not detection but the absence of the coupling: this migration
// cannot be rewritten from another module, so its meaning is fixed by this file alone. The cost it
// accepts, worth knowing: the snapshot silently diverges from the live model over time and nothing
// compares the two. That is intended — the snapshot is frozen by design — but no gate will tell you.
//
// Do not reach for a shared LOCAL base type either. These structs must be free to diverge; that
// divergence is the point, so there is no synchronisation requirement for DRY to serve, and a
// shared base only moves change-at-a-distance one level in — from another module to another
// migration in this package.

// deviceState is the live current-state projection for one device.
//
// tenant_id is declared EXPLICITLY rather than through the tenant-scoped mixin's layout,
// because it has to lead the composite unique index: a device token is unique only per
// tenant (ADR-042), so a bare unique index on device_token would reject a second tenant
// reusing another tenant's token and fail every MergeDeviceState for that device.
type deviceState struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId            string `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:1;not null;size:128"`
	DeviceToken         string `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:2;not null;type:varchar(128)"`
	Active              bool   `gorm:"not null;default:false"`
	LastConnectTime     sql.NullTime
	LastDisconnectTime  sql.NullTime
	LastActivityTime    sql.NullTime
	InactivityAlarmTime sql.NullTime
	InactivityTimeout   int `gorm:"not null;default:600"`

	// Authoritative-presence projection (ADR-067 S2). presence_source defaults to
	// INFERRED so a device with no asserted transition keeps activity-inferred
	// behaviour; session_id is the producer's host-observed connect epoch and orders
	// transitions; presence_time is null until one has been applied.
	PresenceSource string `gorm:"not null;size:16;default:INFERRED"`
	SessionId      int64  `gorm:"not null;default:0"`
	PresenceTime   sql.NullTime

	// Denormalized from device-management at resolve time (ADR-067 SP4b) so Sparkplug
	// failover reconciliation can enumerate a tenant's asserted-active devices by their
	// transport-native identity, and scope that enumeration to its OWN source, without a
	// hop back into device-management.
	ExternalId sql.NullString `gorm:"type:text"`
	Source     sql.NullString `gorm:"type:text"`
}

// latestMeasurement is the current value of each named measurement per device — one row per
// (tenant, device, name) — maintained by the StateProcessor from the resolved-event stream.
type latestMeasurement struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string          `gorm:"not null;type:varchar(128)"`
	Name         string          `gorm:"not null;size:128"`
	Value        sql.NullFloat64 `gorm:"type:decimal(20,8)"`
	Classifier   *uint
	OccurredTime time.Time `gorm:"not null"`

	// The bound metric definition's unit + data type, denormalized (ADR-016) so a
	// last-known value is self-describing without a cross-service hop. Both nullable — an
	// undeclared (unbound) measurement carries neither.
	Unit     sql.NullString `gorm:"type:text"`
	DataType sql.NullString `gorm:"size:32"`
}
