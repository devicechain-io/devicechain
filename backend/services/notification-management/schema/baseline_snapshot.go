// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in model/.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model
// gains a column, the column arrives in a NEW migration appended after the baseline — these
// stay exactly as they are.
//
// Self-contained down to the mixins: gorm.Model, rdb.TenantScoped, rdb.TokenReference,
// rdb.NamedEntity and rdb.MetadataEntity are INLINED rather than embedded, so a change in core
// cannot silently rewrite this migration from a PR that never touched this service. Field
// order matches the embedded layout exactly, because physical column order is part of the
// schema.
//
// 🔴 NO TYPE HERE HAS A TableName METHOD, and the TYPE NAMES ARE LOAD-BEARING BEYOND THE
// TABLE. core/rdb pins the gorm NamingStrategy's TablePrefix to the functional area, and that
// prefix applies only when gorm DERIVES a name — so an explicit TableName would silently
// rename every index on the table. Worse for this area specifically: the derived names here
// run past Postgres's identifier limit, so gorm TRUNCATES AND HASHES them —
// notification_policies' device_type_token index is really
// `idx_notification-management_notification_policies_devic2f007773`. That hash is computed
// from the full derived name, so renaming a type or a field changes the hash and the index
// name with it. Nothing about it is guessable; it is only reproducible by keeping these names
// exactly as they are.

// notificationChannel is a tenant's configured delivery endpoint — SMTP or webhook (ADR-017).
//
// 🔴 THERE IS NO `secret` COLUMN, and its absence is the whole point of one of the migrations
// this baseline replaces. The channel's write-only delivery secret lives envelope-encrypted in
// the shared secret store (ADR-059 S3), keyed by the channel's tenant-scoped handle — not in a
// reversible plaintext column here. Do not add one back to make a read path simpler.
type notificationChannel struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	ChannelType string `gorm:"not null;size:32;index"`
	Config      *datatypes.JSON
	Enabled     bool `gorm:"not null;default:true"`
}

// notificationPolicy is the routing header (ADR-017); the per-severity mappings it owns are
// notificationRule below. The two are created together because a rule has no meaning apart
// from its policy.
type notificationPolicy struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	DeviceTypeToken sql.NullString `gorm:"size:128;index"`
	ThrottleSeconds sql.NullInt64
	Enabled         bool `gorm:"not null;default:true"`

	EscalateAfterSeconds sql.NullInt64
	MaxEscalations       sql.NullInt64
}

// notificationRule maps one severity to a channel plus recipients, within its policy.
type notificationRule struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	PolicyId   uint   `gorm:"not null;index"`
	Severity   string `gorm:"not null;size:16"`
	ChannelId  uint   `gorm:"not null;index"`
	Recipients *datatypes.JSON
}

// notificationState is the per-alarm notification/escalation state the dispatcher upserts and
// the escalation scheduler reads (ADR-017) — one row per raised alarm, keyed by alarm token.
type notificationState struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	AlarmToken string `gorm:"not null;size:128"`
	AlarmKey   string `gorm:"not null;size:256;index"`
	Severity   string `gorm:"size:16"`

	FirstNotifiedAt sql.NullTime
	LastNotifiedAt  sql.NullTime
	NotifyCount     int `gorm:"not null;default:0"`

	AcknowledgedAt sql.NullTime
	ClearedAt      sql.NullTime

	EscalationLevel int `gorm:"not null;default:0"`
	LastEscalatedAt sql.NullTime
}
