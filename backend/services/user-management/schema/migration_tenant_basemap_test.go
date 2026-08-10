// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The same reasoning as migration_tenant_purge_note_test.go: migration-diff gates the
// Postgres schema in CI, but the one mistake this migration is most likely to make —
// losing the pinned TableName — does not FAIL there in a way anyone would notice
// locally. AutoMigrate would create a `tenant_basemap_snapshots` table, report success,
// and leave iam_tenants without the columns every reader and writer now assigns to.
//
// basemapTenantRow is an independent read-back type, deliberately NOT the snapshot the
// migration writes through. Sharing the struct would let a wrong TableName pass by
// being wrong in both places at once.
type basemapTenantRow struct {
	ID    uint
	Token string
	Name  string
	// A tenant's tier is NOT NULL (ADR-065), so every insert here has to name one.
	// Carried on the read-back type rather than worked around, so the test writes a
	// row the real table would accept.
	TierID             uint
	BasemapTileURL     *string
	BasemapAttribution *string
	BasemapCenterLat   *float64
	BasemapCenterLon   *float64
	BasemapZoom        *float64
}

func (basemapTenantRow) TableName() string { return "iam_tenants" }

// seededTierID returns a real tier id from the baseline's seed rather than a
// hard-coded 1, so this test keeps working if the seed order ever changes.
func seededTierID(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	var id uint
	require.NoError(t, db.Raw("SELECT id FROM iam_tenant_tiers ORDER BY id LIMIT 1").Scan(&id).Error)
	require.NotZero(t, id, "the baseline seeds the tier vocabulary; without one no tenant can be inserted")
	return id
}

var basemapColumns = []string{
	"basemap_tile_url", "basemap_attribution",
	"basemap_center_lat", "basemap_center_lon", "basemap_zoom",
}

// TestTheBasemapMigrationAddsTheColumnsToTheTenantTable runs the whole chain and proves
// the columns landed on the tenant table — by writing through them and reading them
// back, not by asking the migrator whether it thinks they exist.
func TestTheBasemapMigrationAddsTheColumnsToTheTenantTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, col := range basemapColumns {
		require.Truef(t, db.Migrator().HasColumn(&basemapTenantRow{}, col),
			"%s must exist on iam_tenants after the chain runs", col)
	}

	tiles := "https://tile.example.invalid/{z}/{x}/{y}.png"
	credit := "© Example"
	lat, lon, zoom := 33.75, -84.39, 11.5
	require.NoError(t, db.Create(&basemapTenantRow{
		Token: "acme", Name: "Acme", TierID: seededTierID(t, db),
		BasemapTileURL: &tiles, BasemapAttribution: &credit,
		BasemapCenterLat: &lat, BasemapCenterLon: &lon, BasemapZoom: &zoom,
	}).Error)

	var back basemapTenantRow
	require.NoError(t, db.First(&back, "token = ?", "acme").Error)
	require.NotNil(t, back.BasemapTileURL)
	require.Equal(t, tiles, *back.BasemapTileURL)
	require.NotNil(t, back.BasemapAttribution)
	require.Equal(t, credit, *back.BasemapAttribution)
	// The camera round-trips as a FLOAT. A column typed as an integer would take 11.5
	// and hand back 11 or 12, silently moving the fallback view — and fractional zoom
	// is ordinary, not an edge case.
	require.NotNil(t, back.BasemapZoom)
	require.Equal(t, 11.5, *back.BasemapZoom)
	require.NotNil(t, back.BasemapCenterLat)
	require.Equal(t, 33.75, *back.BasemapCenterLat)
	require.NotNil(t, back.BasemapCenterLon)
	require.Equal(t, -84.39, *back.BasemapCenterLon)
}

// The columns are NULLABLE, which is what makes "inherit the tier below" expressible at
// all. A NOT NULL column with a zero default would turn every un-configured tenant into
// one that has explicitly chosen 0,0 at zoom 0 — a point in the Atlantic.
func TestTheBasemapColumnsAreNullableSoATenantCanInherit(t *testing.T) {
	db := newMigratedDB(t)

	require.NoError(t, db.Create(&basemapTenantRow{Token: "inherits", Name: "Inherits", TierID: seededTierID(t, db)}).Error)

	var back basemapTenantRow
	require.NoError(t, db.First(&back, "token = ?", "inherits").Error)
	require.Nil(t, back.BasemapTileURL, "an unset tile URL must read back as NULL, not as an empty string")
	require.Nil(t, back.BasemapAttribution)
	require.Nil(t, back.BasemapCenterLat, "an unset centre must read back as NULL, not as 0,0")
	require.Nil(t, back.BasemapCenterLon)
	require.Nil(t, back.BasemapZoom)
}

// TestTheBasemapMigrationDoesNotCreateAStrayTable is the control, and it is the one that
// catches the mistake this migration is actually exposed to. Without the pinned
// TableName, AutoMigrate derives the table from the Go type name and creates a brand-new
// table — succeeding, and leaving the tenant row untouched.
func TestTheBasemapMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, stray := range []string{"tenant_basemap_snapshots", "tenant_basemaps"} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the basemap migration created %q instead of altering iam_tenants — its snapshot has "+
				"lost its pinned TableName, so it migrated nothing", stray)
	}
}

// TestTheBasemapMigrationIsReRunnable pins what the chain requires of every appended
// migration: migrations run with UseTransaction:false and replay from the top after a
// failure, so one that is not individually re-runnable turns a single transient failure
// into a stuck instance.
func TestTheBasemapMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantBasemapMigration().Migrate(db),
		"re-running the basemap migration must be a no-op, not a duplicate-column error")
	for _, col := range basemapColumns {
		require.True(t, db.Migrator().HasColumn(&basemapTenantRow{}, col))
	}
}

// TestTheBasemapMigrationRollbackDropsOnlyItsOwnColumns pins WHICH columns the rollback
// drops. Nothing invokes gormigrate rollbacks today, which is exactly why it is worth a
// test rather than a reading: the day someone reaches for one, a rollback naming a
// neighbouring column would drop a tenant's branding or its governance ceiling, and it
// would do so while succeeding.
func TestTheBasemapMigrationRollbackDropsOnlyItsOwnColumns(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantBasemapMigration().Rollback(db))

	for _, col := range basemapColumns {
		require.Falsef(t, db.Migrator().HasColumn(&basemapTenantRow{}, col),
			"the rollback must drop %q, which this migration added", col)
	}
	for _, kept := range []string{
		"branding_title", "branding_primary", "branding_logo_max_height",
		"ingest_messages_per_second", "outbound_burst", "token", "name",
	} {
		require.Truef(t, db.Migrator().HasColumn(&basemapTenantRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
