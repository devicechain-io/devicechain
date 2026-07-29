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
// written. They are private to this package and are NOT the live models.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model
// gains a column, the column arrives in a NEW migration appended after the baseline — these
// stay exactly as they are. They are wrong about the current schema by design, and that is
// what makes them correct about this migration.
//
// Self-contained down to the mixins: gorm.Model, rdb.TenantScoped, rdb.TokenReference and
// rdb.NamedEntity are INLINED rather than embedded, so a change in core cannot silently
// rewrite this migration from a PR that never touched this service. Field order matches the
// embedded layout exactly, because physical column order is part of the schema.
//
// 🔴 NEITHER TYPE HAS A TableName METHOD. core/rdb pins the gorm NamingStrategy's
// TablePrefix to the functional area, and that prefix applies only when gorm DERIVES a table
// name; an explicit TableName bypasses it and silently renames every index on the table.
// Gorm derives "connectors" and "connector_versions" from the type names, so RENAMING A TYPE
// RETARGETS THE MIGRATION. Leave both the absence and the names alone.

// connector is the mutable draft of a tenant-scoped, versioned outbound-connector
// definition (ADR-060 C4). Config is opaque here — the dispatch side owns its shape, and
// the credential it references lives in the secret store, never in this blob.
type connector struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Type   string         `gorm:"not null;size:64"`
	Config datatypes.JSON `gorm:"not null"`
}

// connectorVersion is an immutable published snapshot of a connector's {type, config},
// addressed by parent + monotonic version (ADR-060 C4).
//
// It carries no token — a version is reached through its parent — so there is no per-tenant
// token index on this table, unlike connector above. The (connector_id, version) uniqueness
// comes from the struct tags via AutoMigrate.
type connectorVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	ConnectorID uint           `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:1"`
	Version     int32          `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:2"`
	Type        string         `gorm:"not null;size:64"`
	Config      datatypes.JSON `gorm:"not null"`
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
	PublishedBy string         `gorm:"size:256"`
}
