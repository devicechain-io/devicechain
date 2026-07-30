// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in events.go.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time.
//
// This area had the WORST snapshot hygiene of the nine, and the flatten is where it gets
// fixed rather than carried forward. Three separate leaks:
//
//   - rdb.TenantScoped was embedded in every event shape, so a core change would rewrite six
//     applied migrations from a PR that never touched this service. INLINED below.
//   - the event_anchors migration ran AutoMigrate against the LIVE model.EventAnchor — a
//     migration whose output changed whenever that struct did. Snapshotted below as
//     eventAnchor.
//   - EventType came from esmodel, a CROSS-SERVICE live package (dc-event-sources). A change
//     to another service's type declaration would silently change this schema. Inlined as
//     int64, which is what esmodel.EventType is declared as and what the column already is
//     (bigint).
//
// 🔴 NO TYPE HERE HAS A TableName METHOD. core/rdb pins the gorm NamingStrategy's TablePrefix
// to the functional area, and that prefix applies only when gorm DERIVES a table name; an
// explicit TableName bypasses it and silently renames every index on the table. Gorm derives
// each table below from the type name, so RENAMING A TYPE RETARGETS THE MIGRATION. Note
// no type here pins one, and the eventAnchor comment records what happened when an earlier
// draft of this file did.
//
// 🔴 NONE OF THE PAYLOAD TABLES DECLARES A PRIMARY KEY, AND THAT IS DELIBERATE. Only `events`
// has one, on (tenant_id, device_token, event_type, occurred_time). The payload tables relate
// to the base event by that same natural key joined at the APP layer, with no database foreign
// key: an FK referencing a hypertable blocks drop_chunks on the parent, which would make the
// retention and compression work un-droppable. Do not "tidy this up" by adding a key or an FK.
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

// eventTypeSnapshot is the frozen numeric event-type discriminator. It is a local alias for
// int64 rather than a use of esmodel.EventType, so this migration cannot be rewritten by a
// change in another service's package. The column is bigint either way.
type eventTypeSnapshot = int64

// event is the base event row. The originating device is keyed by its token (ADR-044).
//
// 🔴 tenant_id LEADS THE COMPOSITE PRIMARY KEY, and that ordering is a correctness property,
// not a style choice. A device token is unique only PER TENANT (ADR-042), so without tenant_id
// in the key two tenants with a same-named device emitting at the same instant collide — and
// upsertParentEvents uses ON CONFLICT DO NOTHING, so one tenant's event is silently dropped
// rather than erroring. It is declared explicitly here (not through the tenant mixin's layout)
// because only this table needs it in the key.
//
// 🔴 THERE ARE NO anchor_type / anchor_id COLUMNS. The chain created that single denormalized
// pair and then dropped it when anchors became a SET in event_anchors (ADR-013 addendum): one
// event can be anchored to a customer AND an area AND an asset, which a single pair cannot
// express. Do not reintroduce them to make a query simpler.
type event struct {
	TenantId      string            `gorm:"primaryKey;not null;size:128"`
	DeviceToken   string            `gorm:"primaryKey;type:varchar(128)"`
	EventType     eventTypeSnapshot `gorm:"primaryKey"`
	OccurredTime  time.Time         `gorm:"primaryKey"`
	Source        string
	AltId         sql.NullString
	ProcessedTime time.Time
}

// locationEvent carries a position reading. Elevation is METRES, not degrees — hence the
// different precision from latitude/longitude.
type locationEvent struct {
	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    eventTypeSnapshot `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Latitude     sql.NullFloat64   `gorm:"type:decimal(10,8);"`
	Longitude    sql.NullFloat64   `gorm:"type:decimal(11,8);"`
	Elevation    sql.NullFloat64   `gorm:"type:decimal(12,4);"`
}

// measurementEvent carries one named numeric reading.
//
// unit and data_type are the bound metric definition's, denormalized at write time (ADR-016):
// the classifier already links to that definition, but it lives in device-management, so
// resolving on read would be a cross-service hop — and storing them keeps a historical row's
// unit stable across a later profile republish. Both nullable (an unbound measurement carries
// neither), and both LAST, because they arrived in a later additive migration and AutoMigrate
// appends.
type measurementEvent struct {
	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    eventTypeSnapshot `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Name         string            `gorm:"not null"`
	Value        sql.NullFloat64   `gorm:"type:decimal(20,8);"`
	Classifier   *uint

	Unit     sql.NullString `gorm:"type:text"`
	DataType sql.NullString `gorm:"size:32"`
}

// alertEvent carries a device-reported alert.
type alertEvent struct {
	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    eventTypeSnapshot `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Type         string            `gorm:"not null"`
	Level        uint32            `gorm:"not null"`
	Message      string
	Source       string
}

// eventAnchor is one relationship anchor for an event (ADR-013 addendum) — the set that
// replaced the base event's single denormalized pair. Both the source device and the anchor
// target are named by their stable per-tenant tokens (ADR-044), never device-management row ids.
//
// 🔴 IT MUST BE NAMED THIS, WITH NO TableName METHOD, AND THAT WAS LEARNED THE HARD WAY IN THIS
// VERY FILE. The first version of this snapshot was called `eventAnchorRow` with an explicit
// `TableName() { return "event_anchors" }`, on the reasoning that a distinct name stops a reader
// confusing it with the live model.EventAnchor the replaced migration wrongly used. The table
// name came out right and the INDEX NAME DID NOT: gorm applies core/rdb's area TablePrefix only
// to names it DERIVES, so pinning TableName produced
//
//	idx_event_anchors_tenant_id                    (wrong)
//	idx_event-management_event_anchors_tenant_id    (what the chain built)
//
// for the tenant_id index — one index, silently renamed, on a table whose every other column
// was correct. migration-diff caught it; nothing else would have. The lesson is that the trap
// this file warns about above is not hypothetical even to someone writing the warning: let gorm
// derive, and separate the snapshot from the live model by the lowercase name and this comment
// rather than by a method that changes the schema.
type eventAnchor struct {
	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    eventTypeSnapshot `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	AnchorType   string            `gorm:"not null"`
	AnchorToken  string            `gorm:"type:varchar(128);not null"`
}

// stateChangeEvent is the append-only presence history (ADR-067 decision 5, S3): the queryable
// connect/disconnect timeline the live device-state projection cannot provide.
//
// Its idempotency index (see the baseline) exists because a StateChange carries no AltId, so
// the base-event dedup key never engages and a JetStream redelivery would otherwise write a
// duplicate presence row.
type stateChangeEvent struct {
	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    eventTypeSnapshot `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	State        string            `gorm:"type:varchar(16);not null"`
	Reason       string
	SessionId    uint64 `gorm:"not null;default:0"`
}
