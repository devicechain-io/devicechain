// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantBasemapSnapshot is this migration's own snapshot of iam_tenants: the primary
// key, and the five columns it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go. Pointing
// this at iam.Tenant would make the migration mean whatever that struct means on the
// day it runs — which breaks FRESH installs the moment the tenant gains another field
// (the baseline creates the table without it, this migration adds it, and the next
// fresh install replays into "column already exists") while every existing database
// applies cleanly and looks healthy.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type
// name and this migration silently creates a `tenant_basemap_snapshots` table instead
// of touching the tenant table — succeeding, migrating nothing, and leaving the columns
// that every reader and writer above it now assigns to.
// TestTheBasemapMigrationAddsTheColumnsToTheTenantTable is the guard, and it asserts
// the absence of the stray table as well as the presence of the columns.
type tenantBasemapSnapshot struct {
	ID uint `gorm:"primarykey"`

	// Untagged nullable columns, matching the branding override block beside them: a
	// null field means "inherit the tier below". The two halves of the tile source are
	// stored as separate columns but are NOT independently inheritable — that rule
	// lives in basemap.Merge, not in the schema, because it is a property of the
	// cascade rather than of the storage.
	BasemapTileURL     *string
	BasemapAttribution *string
	BasemapCenterLat   *float64
	BasemapCenterLon   *float64
	BasemapZoom        *float64
}

func (tenantBasemapSnapshot) TableName() string { return "iam_tenants" }

// NewTenantBasemapMigration adds the per-tenant basemap override columns (ADR-079).
//
// The basemap was a per-BROWSER preference: the geofence editor kept its tile URL and
// credit line in localStorage, and the dashboard map widget carried the same values as
// per-widget options. So every operator in a tenant pasted the same provider details by
// hand, a new one saw a blank map and reasonably concluded it was broken, and the same
// provider credential was re-entered on every dashboard with no single place to rotate
// it. These columns are where a tenant's own choice lives.
//
// AutoMigrate is individually re-runnable — it adds each column if absent and is a
// no-op if present — which the chain requires, since migrations run with
// UseTransaction:false and replay from the top after a failure.
func NewTenantBasemapMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260810120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantBasemapSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{
				"basemap_tile_url", "basemap_attribution",
				"basemap_center_lat", "basemap_center_lon", "basemap_zoom",
			} {
				if err := tx.Migrator().DropColumn(&tenantBasemapSnapshot{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
