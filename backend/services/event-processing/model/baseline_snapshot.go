// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in model.go or the
// store types alongside them.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model
// gains a column, the column arrives in a NEW migration appended after the baseline — these
// stay exactly as they are.
//
// This area embeds no core mixins at all — no gorm.Model, no rdb.TenantScoped — so unlike
// the other baselines there is nothing to inline. That is a property of the engine's tables
// rather than an oversight: they are read-models keyed by their own natural keys, they are
// not soft-deletable, and the tenant is a plain column named `tenant` (not `tenant_id`)
// because these tables are NOT tenant-scoped in the rdb sense — the DETECT engine is a
// per-Instance singleton that spans every tenant, so the global tenant-scope callback must
// not apply to them. Do not "regularise" that to rdb.TenantScoped: it would rename the
// column and pull the tables into a scoping callback that would then filter the engine's own
// cross-tenant reads down to nothing.
//
// 🔴 NO TYPE HERE HAS A TableName METHOD. core/rdb pins the gorm NamingStrategy's TablePrefix
// to the functional area, and that prefix applies only when gorm DERIVES a table name; an
// explicit TableName bypasses it and silently renames every index on the table
// (idx_event-processing_detect_rules_tenant → idx_detect_rules_tenant). Gorm derives each
// table name from the type name below, so RENAMING A TYPE RETARGETS THE MIGRATION. Note
// `profileActive` → "profile_actives" and `deviceRoster` → "device_rosters": the pluralisation
// is gorm's, and both are the names the stores query.
//

// detectSnapshot is one durable checkpoint per partition (ADR-051): partition_id is the
// natural primary key, since a partition has exactly one live checkpoint overwritten in place
// on each commit. Payload holds the serialized engine state and StreamSeq the highest applied
// JetStream sequence captured in it (ack-on-checkpoint).
//
// A plain relational table, not a hypertable — bounded by the number of single-writer
// partitions (one per Instance at GA), so it never grows with telemetry volume.
type detectSnapshot struct {
	PartitionId string    `gorm:"primaryKey;size:128;not null"`
	StreamSeq   int64     `gorm:"not null"`
	Watermark   time.Time `gorm:"not null"`
	Payload     []byte    `gorm:"not null"`
	UpdatedAt   time.Time
}

// detectRule is the durable detection-rule projection (ADR-051 4b-3): the rule set the engine
// rebuilds from at startup, so a rule survives a restart independent of the finite-retention
// fact stream.
//
// 🔴 THE TWO SCOPE COLUMNS ARE LAST, AND THAT IS THE SCHEMA. They arrived in a later additive
// migration (ADR-062 S4) and AutoMigrate appends, so entity_group_token and
// entity_group_version sit after updated_at — not beside rule_token where a fresh reading
// would group them. AutoMigrate emits columns in struct-field order, so moving them changes
// the schema.
//
// Their defaults are load-bearing too: empty token / version 0 means UNSCOPED, so a row
// written before the scope existed reads back as the profile-wide rule it was. The scope has
// to live in this projection precisely because the engine rebuilds from it — a scope threaded
// only from the live fact would be lost on reload, silently un-scoping every scoped rule
// (fires fleet-wide, never descopes).
type detectRule struct {
	RuleId              string `gorm:"primaryKey;size:512;not null"`
	Tenant              string `gorm:"size:256;not null;index"`
	ProfileVersionToken string `gorm:"size:256;not null"`
	RuleToken           string `gorm:"size:256;not null"`
	Definition          string `gorm:"not null"`
	UpdatedAt           time.Time
	EntityGroupToken    string `gorm:"size:256;not null;default:''"`
	EntityGroupVersion  int32  `gorm:"not null;default:0"`
}

// deviceRoster is the set of devices expected to report, so the dead-man roster can arm
// absence for a device that has never reported at all (ADR-051 4c-2b).
type deviceRoster struct {
	Tenant        string    `gorm:"primaryKey;size:256;not null"`
	DeviceToken   string    `gorm:"primaryKey;size:256;not null"`
	ProfileToken  string    `gorm:"size:256;not null;index"`
	ExpectedSince time.Time `gorm:"not null"`
	Deleted       bool      `gorm:"not null"`
	LastEventAt   time.Time `gorm:"not null"`
	UpdatedAt     time.Time
}

// profileActive records which published version is active per stable profile token, plus its
// publish time — the grace-period base for absence arming (ADR-051 4c-2b).
type profileActive struct {
	Tenant             string    `gorm:"primaryKey;size:256;not null"`
	ProfileToken       string    `gorm:"primaryKey;size:256;not null"`
	ActiveVersionToken string    `gorm:"size:256;not null"`
	PublishedAt        time.Time `gorm:"not null"`
	UpdatedAt          time.Time
}

// deviceAttribute is the current numeric value of a platform-set device attribute, so a
// detection rule can resolve a threshold from a device's own attribute (ADR-051 4c-3, the CEL
// "attr" var).
type deviceAttribute struct {
	Tenant      string    `gorm:"primaryKey;size:256;not null"`
	DeviceToken string    `gorm:"primaryKey;size:256;not null"`
	Scope       string    `gorm:"primaryKey;size:64;not null"`
	AttrKey     string    `gorm:"primaryKey;size:256;not null"`
	Value       float64   `gorm:"not null"`
	Deleted     bool      `gorm:"not null"`
	LastEventAt time.Time `gorm:"not null"`
	UpdatedAt   time.Time
}

// deviceAttributeDeletion is the per-device resurrection fence a device deletion drops. It
// exists because the deletion fact carries no attribute keys, so there is nothing to mark
// deleted one row at a time.
type deviceAttributeDeletion struct {
	Tenant      string    `gorm:"primaryKey;size:256;not null"`
	DeviceToken string    `gorm:"primaryKey;size:256;not null"`
	DeletedAt   time.Time `gorm:"not null"`
	UpdatedAt   time.Time
}

// ruleStat is the durable per-rule firing projection (ADR-051 7b), upserted inline by the
// single-writer publish loop and read by the console's rule-health view. A counting
// read-model, engine-inert.
type ruleStat struct {
	RuleId      string    `gorm:"primaryKey;size:512;not null"`
	Tenant      string    `gorm:"size:256;not null;index"`
	LastFiredAt time.Time `gorm:"not null"`
	FireCount   int64     `gorm:"not null"`
	LastEdge    string    `gorm:"size:16;not null"`
}
