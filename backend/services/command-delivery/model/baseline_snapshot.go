// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structure the BASELINE migration creates, captured at the point in time it was
// written. It is private to this package and is NOT the live model in model.go.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live model is the current incarnation of this
// datatype; this is a snapshot, and the difference between them is time. When the model
// gains a column, the column arrives in a NEW migration appended after the baseline — this
// stays exactly as it is. It is wrong about the current schema by design, and that is what
// makes it correct about this migration.
//
// It is self-contained down to the mixins: gorm.Model, rdb.TenantScoped,
// rdb.TokenReference and rdb.MetadataEntity are INLINED rather than embedded. Embedding
// them would leave a change in core silently rewriting this migration — a fresh install
// would create the new column here, the migration meant to add it would then hit "column
// already exists", and the diff carrying that breakage would be in backend/core, in a PR
// that never touched this service. Field order matches the embedded layout exactly,
// because physical column order is part of the schema this reproduces.
//
// 🔴 IT HAS NO TableName METHOD, and that is load-bearing rather than an omission.
// core/rdb pins the gorm NamingStrategy's TablePrefix to the functional area, and that
// prefix applies only when gorm DERIVES a table name. An explicit TableName bypasses it,
// so the indexes come out unprefixed:
//
//	no TableName  → idx_command-delivery_commands_status   (what this table has)
//	TableName()   → idx_commands_status
//
// Adding one here would silently rename every index on the table. Gorm derives "commands"
// from the type name, so RENAMING THE TYPE RETARGETS THE MIGRATION. Leave both the absence
// and the name alone.
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

// command is a persisted, lifecycle-tracked command to a device (ADR-012 #4). A plain
// relational table, not a hypertable.
//
// 🔴 THERE IS NO delivered_time COLUMN, and its absence is part of what this baseline
// froze. The chain created it and then dropped it: confirming delivery distinctly from a
// response needs a device- or broker-level acknowledgment, no such transport exists, and
// nothing ever wrote the column or the DELIVERED status it existed to timestamp. Do not
// "restore" it to match some older reading of the model.
type command struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Metadata *datatypes.JSON

	DeviceToken     string `gorm:"index;not null;size:128"`
	Name            string `gorm:"not null;size:128"`
	Payload         *datatypes.JSON
	Status          string `gorm:"index;not null;size:32"`
	QueuedTime      time.Time
	SentTime        sql.NullTime
	RespondedTime   sql.NullTime
	ExpiresAt       sql.NullTime
	ResponsePayload *datatypes.JSON
	Error           sql.NullString
}
