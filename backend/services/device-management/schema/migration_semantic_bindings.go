// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// semanticContext is the SNAPSHOT of a tenant's semantic context as of this migration. It is
// not model.SemanticContext and must never be replaced by it: a migration's shapes are a
// point in time, and pointing one at a live model silently rewrites an already-applied
// migration whenever that model changes, which breaks FRESH installs while every existing
// database applies cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped) are INLINED, matching every other shape in this
// package, and field order matches the embedded layout because physical column order is part
// of the schema. There is no TableName(), for the reason maintenanceWindow records.
//
// enabled_packs is a text[] declared by tag, which gorm hands to the database unchanged.
// custom_prefixes ({prefix: namespace}) and pack_versions ({pack id: version recorded when the
// context was set}) are plain jsonb documents that are read and validated whole; nothing in
// SQL looks inside them.
type semanticContext struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	EnabledPacks   string         `gorm:"type:text[];not null"`
	CustomPrefixes datatypes.JSON `gorm:"not null"`
	PackVersions   datatypes.JSON `gorm:"not null"`
}

// NewSemanticBindingsSchema adds the semantic layer's storage: the per-tenant semantic
// context, and the nullable binding columns on the native model.
//
// This is an APPENDED migration; the baseline is frozen. Every added column is nullable with
// no default: NULL says "nothing is bound", which is the true state of every row that
// predates the feature, so there is no backfill, and an old pod's writes (which never name
// these columns) keep succeeding while old and new pods overlap during a rolling upgrade.
//
//   - semantic_types text[] on device_types, asset_types, area_types and customer_types, and
//     on asset_type_versions, which freezes the binding beside the property schema so it
//     publishes and rolls back with the type;
//   - semantic_pack_versions jsonb on asset_type_versions: the vocabulary versions the
//     frozen types were validated against;
//   - semantic_predicate and semantic_inverse text on entity_relationship_types;
//   - semantic_property and semantic_point_class text on metric_definitions.
//
// Values are EXPANDED IRIs, so they are text however long a namespace is; nothing is sized.
//
// # The columns are literal DDL, not snapshot structs
//
// The tables already exist, and a second Go shape mapping to one would need a TableName()
// that bypasses this area's prefix (see assetPropertyColumns). Writing the column out is the
// snapshot.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. The table
// is AutoMigrate (a no-op when present), the unique index is IF NOT EXISTS, and every column
// is ADD COLUMN IF NOT EXISTS.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it directly with
// no exemption to register.
func NewSemanticBindingsSchema() *gormigrate.Migration {
	const schemaName = `"device-management"`
	statements := []string{
		`ALTER TABLE ` + schemaName + `.device_types ADD COLUMN IF NOT EXISTS semantic_types text[];`,
		`ALTER TABLE ` + schemaName + `.asset_types ADD COLUMN IF NOT EXISTS semantic_types text[];`,
		`ALTER TABLE ` + schemaName + `.area_types ADD COLUMN IF NOT EXISTS semantic_types text[];`,
		`ALTER TABLE ` + schemaName + `.customer_types ADD COLUMN IF NOT EXISTS semantic_types text[];`,
		`ALTER TABLE ` + schemaName + `.asset_type_versions ADD COLUMN IF NOT EXISTS semantic_types text[];`,
		`ALTER TABLE ` + schemaName + `.asset_type_versions ADD COLUMN IF NOT EXISTS semantic_pack_versions jsonb;`,
		`ALTER TABLE ` + schemaName + `.entity_relationship_types ADD COLUMN IF NOT EXISTS semantic_predicate text;`,
		`ALTER TABLE ` + schemaName + `.entity_relationship_types ADD COLUMN IF NOT EXISTS semantic_inverse text;`,
		`ALTER TABLE ` + schemaName + `.metric_definitions ADD COLUMN IF NOT EXISTS semantic_property text;`,
		`ALTER TABLE ` + schemaName + `.metric_definitions ADD COLUMN IF NOT EXISTS semantic_point_class text;`,
	}
	return &gormigrate.Migration{
		ID: "20261120120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&semanticContext{}); err != nil {
				return err
			}
			// One context per tenant among LIVE rows.
			if err := createPartialUniqueIndexWhere(tx, &semanticContext{},
				"uix_semantic_contexts_tenant", "deleted_at IS NULL", "tenant_id"); err != nil {
				return err
			}
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Only the table is undone; the nullable columns are left in place (dropping a column
		// destroys data, and a rollback exists to undo a failed forward migration rather than
		// to erase what ran after it).
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"semantic_contexts"})
		},
	}
}
