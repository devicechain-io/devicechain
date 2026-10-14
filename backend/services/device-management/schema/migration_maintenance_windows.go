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

// maintenanceWindow is the SNAPSHOT of the maintenance-window entity as of this migration.
// It is not model.MaintenanceWindow and must never be replaced by it: a migration's shapes
// are a point in time, and pointing one at a live model silently rewrites an
// already-applied migration whenever that model changes — which breaks FRESH installs while
// every existing database applies cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped, rdb.TokenReference, rdb.NamedEntity,
// rdb.MetadataEntity) are INLINED rather than embedded, matching every other shape in this
// package. Field order matches the embedded layout exactly, because physical column order
// is part of the schema.
//
// 🔴 NO TableName(), for the reason geoFence records: a pinned name on a shape no other
// shape describes would make gorm derive UNPREFIXED index names here while every sibling
// table gets prefixed ones.
//
// slots is a plain jsonb document: it is read and validated whole and nothing in SQL ever
// looks inside it.
type maintenanceWindow struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	Timezone string         `gorm:"not null;size:64"`
	Slots    datatypes.JSON `gorm:"not null"`
}

// NewMaintenanceWindowsSchema creates the maintenance-window table and the device type's
// reference to it.
//
// This is an APPENDED migration; the baseline is frozen. The table comes BEFORE the
// reference because the foreign key names it.
//
// # The reference is literal DDL, not a snapshot struct
//
// device_types already exists, and a second Go shape mapping to it would need a TableName()
// that bypasses this area's prefix (see assetPropertyColumns). Writing the column out is the
// snapshot. It is NULLABLE with no default, which is also what makes a backfill
// unnecessary: NULL says "this type has no window", which is the true state of every type
// that predates the feature.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. The
// table is AutoMigrate (a no-op when present), every other statement is IF NOT EXISTS, and
// the constraint is guarded by a catalog lookup because ADD CONSTRAINT has no IF NOT EXISTS
// form.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it directly
// with no exemption to register.
func NewMaintenanceWindowsSchema() *gormigrate.Migration {
	statements := []string{
		`ALTER TABLE "device-management".device_types ADD COLUMN IF NOT EXISTS maintenance_window_id bigint;`,
		`CREATE INDEX IF NOT EXISTS "idx_device-management_device_types_maintenance_window_id" ` +
			`ON "device-management".device_types (maintenance_window_id);`,
		`DO $$ BEGIN ` +
			`IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_device-management_device_types_maintenance_window') THEN ` +
			`ALTER TABLE "device-management".device_types ` +
			`ADD CONSTRAINT "fk_device-management_device_types_maintenance_window" ` +
			`FOREIGN KEY (maintenance_window_id) REFERENCES "device-management".maintenance_windows (id); ` +
			`END IF; END $$;`,
	}
	return &gormigrate.Migration{
		ID: "20261014000100",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&maintenanceWindow{}); err != nil {
				return err
			}
			// A token is unique within a tenant among LIVE rows only. The helper is the
			// baseline's deliberate local copy.
			if err := createTenantTokenIndex(tx, &maintenanceWindow{}); err != nil {
				return err
			}
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Only the table and constraint are undone; the nullable column is left in place
		// (dropping a column destroys data, and a rollback exists to undo a failed forward
		// migration rather than to erase what ran after it).
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE "device-management".device_types ` +
				`DROP CONSTRAINT IF EXISTS "fk_device-management_device_types_maintenance_window";`).Error; err != nil {
				return err
			}
			return dropTables(tx, []string{"maintenance_windows"})
		},
	}
}

// NewCommandImpactSchema adds command_definitions.impact: how disruptive the command is
// (NORMAL, DISRUPTIVE or DESTRUCTIVE). Only the last two are held for a maintenance window.
//
// Nullable with no default, for the reason migration_device_disabled.go gives (a constant
// default is stored in the catalog rather than the rows and is lost by a logical dump and
// restore). NULL is read as NORMAL, which is the true state of every definition that
// predates the field, so there is no backfill. One statement, IF NOT EXISTS, hence
// re-runnable.
func NewCommandImpactSchema() *gormigrate.Migration {
	const table = `"device-management"."command_definitions"`
	return &gormigrate.Migration{
		ID: "20261014000200",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN IF NOT EXISTS impact varchar(16);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` DROP COLUMN IF EXISTS impact;`).Error
		},
	}
}
