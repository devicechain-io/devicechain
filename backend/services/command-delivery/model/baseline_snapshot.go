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
