// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// assetTypeVersion is the SNAPSHOT of the asset-type published-version table as of
// this migration (ADR-072). It is not model.AssetTypeVersion and must never be
// replaced by it: a migration's shapes are a point in time, and pointing one at a
// live model silently rewrites an already-applied migration whenever that model
// changes — which breaks FRESH installs while every existing database applies
// cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped) are INLINED rather than embedded,
// matching every other shape in this package, so a field added to a core mixin
// cannot rewrite this migration from a PR that never touched device-management.
//
// # What the table is
//
// One row per publish of an asset type's property contract: the frozen
// []ParameterSpec document its assets are validated against. Immutable and
// append-only — there is no update path to it anywhere in the API or the schema —
// which is what lets an asset written months ago name the contract it satisfied.
//
// # Why it has no token
//
// Every registry entity in this area carries one and this deliberately does not, so
// its absence from tokenIndexModels' successor list is by design rather than by
// oversight. A token is an ADDRESS a caller re-points; a version is addressed only
// as (its type, its number), and giving it a token would create a re-pointing
// surface for a row whose whole value is that it never moves. The four versioning
// tables that came before it make the same choice.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it
// directly with no exemption to register. That is checked by the coverage gate that
// runs alongside migration-diff verify, not asserted here.
type assetTypeVersion struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	// Names fk_device-management_asset_type_versions_asset_type. The FK target is the
	// BASELINE's asset-type snapshot, used here only to name the reference — exactly
	// how the device-replacement journal points at `device`. Nothing about asset_types
	// is re-asserted by that reference.
	AssetTypeId uint `gorm:"not null;uniqueIndex:uix_asset_type_versions_type_version,priority:1"`
	AssetType   *assetType
	Version     int32 `gorm:"not null;uniqueIndex:uix_asset_type_versions_type_version,priority:2"`

	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	PropertySchema datatypes.JSON `gorm:"not null"`

	PublishedBy string `gorm:"size:256"`
}

// assetPropertyColumns are the three columns this migration adds to tables the
// baseline already created.
//
// 🔴 THEY ARE RAW DDL RATHER THAN A SNAPSHOT STRUCT, and that is the same decision
// NewListOrderIndexesSchema records rather than a departure from the snapshot rule.
// gorm derives a table name from the TYPE name, so a second Go shape mapping to
// `asset_types` needs a TableName() — and a TableName() bypasses this area's
// TablePrefix, so the statement would be issued against an UNQUALIFIED asset_types
// resolved through search_path instead of "device-management".asset_types. Writing
// the column, its type and its table out literally IS the snapshot here: complete,
// frozen, and unable to drift toward a live model because there is no Go type for a
// live model to be swapped in for.
//
// The types match what the baseline's own columns of the same kind produced —
// `jsonb` for a *datatypes.JSON, `integer` for a sql.NullInt32 — so the appended
// columns are indistinguishable from ones the baseline had created itself.
//
// All three are NULLABLE with no default, which is what makes the backfill question
// answerable rather than deferred: NULL is the correct value for every pre-existing
// row. A type that predates this declares no property contract (NULL property_schema),
// has never been published (NULL active_version), and its assets carry no properties
// (NULL properties). Defaulting property_schema to an empty array instead would be a
// STRONGER claim than the data supports — an empty contract REFUSES properties, where
// no contract merely has none.
var assetPropertyColumns = []string{
	`ALTER TABLE "device-management".asset_types ADD COLUMN IF NOT EXISTS property_schema jsonb;`,
	`ALTER TABLE "device-management".asset_types ADD COLUMN IF NOT EXISTS active_version integer;`,
	`ALTER TABLE "device-management".assets ADD COLUMN IF NOT EXISTS properties jsonb;`,
}

// NewAssetPropertySchemaSchema adds the asset-type property contract (ADR-072): a
// draft schema and a published-version pointer on asset_types, a property document
// on assets, and the immutable version table the pointer names.
//
// This is an APPENDED migration. The baseline is frozen — its table list and its
// snapshot structs stay exactly as they are — and this runs after it, which is the
// same position a fresh install and an upgraded one both put it in. Physical order
// is part of what the golden schema diff compares.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure,
// so this must be individually re-runnable. Each ALTER carries IF NOT EXISTS, which
// makes a replay a no-op; AutoMigrate creates a table only when it is absent and adds
// only the columns that are missing. The columns come BEFORE the table because the
// table's foreign key names asset_types, and nothing in it depends on a statement
// after it.
//
// # No backfill, and the claim is the narrow one
//
// "Pre-GA an instance is recreated" is true of a BASELINE and false of an appended
// migration — v0.11.0 and v0.12.0 were each reached with `helm upgrade`, so this does
// run against databases holding real data. What makes a backfill unnecessary here is
// specific and is written out on assetPropertyColumns above: NULL is not a placeholder
// for a value that should have been computed, it is the correct reading of every row
// that predates the feature.
func NewAssetPropertySchemaSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260904180000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range assetPropertyColumns {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return tx.AutoMigrate(&assetTypeVersion{})
		},
		// Only the new table is dropped. The three added columns are left in place:
		// dropping a column destroys data, and a rollback exists to undo a failed
		// forward migration rather than to erase what ran successfully after it.
		// Re-runnable in this direction too, which the UseTransaction:false doctrine
		// requires of both.
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"asset_type_versions"})
		},
	}
}
