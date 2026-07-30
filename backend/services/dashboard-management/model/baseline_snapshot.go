// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in model.go.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of
// these datatypes; these are a snapshot, and the difference between them is time. Today
// they describe the same tables. When a model gains a column, the column arrives in a NEW
// migration appended after the baseline — these stay exactly as they are. They are wrong
// about the current schema by design, and that is what makes them correct about this
// migration.
//
// They are self-contained down to the mixins: gorm.Model, rdb.TenantScoped,
// rdb.TokenReference and rdb.NamedEntity are INLINED rather than embedded. Embedding them
// would leave a change in core silently rewriting this migration — a fresh install would
// create the new column here, the migration meant to add it would then hit "column already
// exists", and the diff carrying that breakage would be in backend/core, in a PR that
// never touched this service. Field order matches the embedded layout exactly, because
// physical column order is part of the schema this reproduces.
//
// 🔴 NEITHER TYPE HAS A TableName METHOD, and that is load-bearing rather than an
// omission. core/rdb pins the gorm NamingStrategy's TablePrefix to the functional area,
// and that prefix is applied only when gorm DERIVES a table name. An explicit TableName
// bypasses it, so the table resolves through search_path instead and its indexes come out
// unprefixed:
//
//	no TableName  → idx_dashboard-management_dashboards_token   (what these tables have)
//	TableName()   → idx_dashboards_token
//
// Adding one here would silently rename every index on the table. Gorm derives
// "dashboards" and "dashboard_versions" from the type names below, so RENAMING A TYPE
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

// dashboard is the persisted dashboard definition (ADR-039). A plain relational table,
// one row per dashboard — not a hypertable.
type dashboard struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	// Opaque to this service: the runtime owns the definition's shape, and versioning it
	// as a blob is the ADR-039 decision rather than a shortcut.
	Definition datatypes.JSON `gorm:"not null"`
}

// dashboardVersion is an immutable published snapshot of a dashboard's definition,
// addressed by parent + monotonic version (ADR-039 versioning).
//
// It carries no token — a version is reached through its parent, so there is no
// per-tenant token index on this table, unlike dashboard above. The
// (dashboard_id, version) uniqueness comes from the struct tags via AutoMigrate.
type dashboardVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DashboardID uint           `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:1"`
	Version     int32          `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:2"`
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
	Definition  datatypes.JSON `gorm:"not null"`
	PublishedBy string         `gorm:"size:256"`
}
