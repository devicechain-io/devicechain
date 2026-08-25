// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The same reasoning as migration_tenant_held_command_ceiling_test.go: migration-diff gates the
// Postgres schema in CI, but the one mistake this migration is most likely to make — losing the
// pinned TableName — does not FAIL there in a way anyone would notice locally. AutoMigrate would
// create a `tenant_geo_fence_caps_snapshots` table, report success, and leave iam_tenants
// without the three columns every reader and writer now assigns to.
//
// geoFenceCapsTenantRow is an independent read-back type, deliberately NOT the snapshot the
// migration writes through. Sharing the struct would let a wrong TableName pass by being wrong
// in both places at once.
type geoFenceCapsTenantRow struct {
	ID    uint
	Token string
	Name  string
	// A tenant's tier is NOT NULL (ADR-065), so every insert here has to name one.
	TierID                uint
	GeoFenceVertexCeiling *int
	GeoFenceCeiling       *int
	GeoFenceVertexBudget  *int
}

func (geoFenceCapsTenantRow) TableName() string { return "iam_tenants" }

// TestTheGeoFenceCapsMigrationAddsAllThreeColumnsToTheTenantTable runs the whole chain and
// proves the columns landed on the tenant table — by writing through them and reading them
// back, not by asking the migrator whether it thinks they exist.
//
// 🔴 IT READS EACH VALUE BACK DISTINCTLY, and that is not ceremony. Three same-typed nullable
// int columns with similar names is exactly the shape in which one AutoMigrate field lands in
// another column: every "does it exist?" check would pass, every write would succeed, and a
// tenant's whole-set budget would be enforced as its per-fence ceiling. The three values below
// are deliberately far apart so a cross shows up as a wrong number rather than a wrong nil.
func TestTheGeoFenceCapsMigrationAddsAllThreeColumnsToTheTenantTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, col := range []string{"geo_fence_vertex_ceiling", "geo_fence_ceiling", "geo_fence_vertex_budget"} {
		require.Truef(t, db.Migrator().HasColumn(&geoFenceCapsTenantRow{}, col),
			"%s must exist on iam_tenants after the chain runs", col)
	}

	tierID := seededTierID(t, db)
	vertexCeiling, fenceCeiling, budget := 700, 250, 90000
	require.NoError(t, db.Create(&geoFenceCapsTenantRow{
		Token: "capped", Name: "Capped", TierID: tierID,
		GeoFenceVertexCeiling: &vertexCeiling,
		GeoFenceCeiling:       &fenceCeiling,
		GeoFenceVertexBudget:  &budget,
	}).Error)

	var back geoFenceCapsTenantRow
	require.NoError(t, db.First(&back, "token = ?", "capped").Error)
	require.NotNil(t, back.GeoFenceVertexCeiling)
	require.Equal(t, 700, *back.GeoFenceVertexCeiling, "geo_fence_vertex_ceiling read back another column's value")
	require.NotNil(t, back.GeoFenceCeiling)
	require.Equal(t, 250, *back.GeoFenceCeiling, "geo_fence_ceiling read back another column's value")
	require.NotNil(t, back.GeoFenceVertexBudget)
	require.Equal(t, 90000, *back.GeoFenceVertexBudget, "geo_fence_vertex_budget read back another column's value")

	// A tenant that declares none reads back NULL — inherit — not zero. The distinction is the
	// whole cascade: zero would be a cap of "no fence permitted", which is the one reading of
	// an unconfigured tenant that is an outage rather than a default.
	require.NoError(t, db.Create(&geoFenceCapsTenantRow{
		Token: "inherits-caps", Name: "Inherits", TierID: tierID,
	}).Error)
	var inherits geoFenceCapsTenantRow
	require.NoError(t, db.First(&inherits, "token = ?", "inherits-caps").Error)
	require.Nil(t, inherits.GeoFenceVertexCeiling, "an unset vertex ceiling must read back NULL (inherit), not 0")
	require.Nil(t, inherits.GeoFenceCeiling, "an unset fence ceiling must read back NULL (inherit), not 0")
	require.Nil(t, inherits.GeoFenceVertexBudget, "an unset vertex budget must read back NULL (inherit), not 0")

	// A tenant may declare SOME of the three. Each column inherits independently — there is no
	// all-or-nothing group — so a partial row must leave the others null rather than defaulting
	// them alongside the one that was set.
	require.NoError(t, db.Create(&geoFenceCapsTenantRow{
		Token: "partial-caps", Name: "Partial", TierID: tierID, GeoFenceCeiling: &fenceCeiling,
	}).Error)
	var partial geoFenceCapsTenantRow
	require.NoError(t, db.First(&partial, "token = ?", "partial-caps").Error)
	require.NotNil(t, partial.GeoFenceCeiling)
	require.Equal(t, 250, *partial.GeoFenceCeiling)
	require.Nil(t, partial.GeoFenceVertexCeiling, "setting one cap must not populate the others")
	require.Nil(t, partial.GeoFenceVertexBudget, "setting one cap must not populate the others")
}

// TestTheGeoFenceCapsMigrationDoesNotCreateAStrayTable is the control, and it is the one that
// catches the mistake this migration is actually exposed to. Without the pinned TableName,
// AutoMigrate derives the table from the Go type name and creates a brand-new table —
// succeeding, and leaving the tenant row untouched.
func TestTheGeoFenceCapsMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, stray := range []string{
		"tenant_geo_fence_caps_snapshots", "tenant_geo_fence_caps_snapshot", "tenant_geo_fence_caps",
	} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the geofence-caps migration created %q instead of altering iam_tenants — its snapshot "+
				"has lost its pinned TableName, so it migrated nothing", stray)
	}
}

// TestTheGeoFenceCapsMigrationIsReRunnable pins what the chain requires of every appended
// migration: migrations run with UseTransaction:false and replay from the top after a failure,
// so one that is not individually re-runnable turns a single transient failure into a stuck
// instance.
func TestTheGeoFenceCapsMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantGeoFenceCapsMigration().Migrate(db),
		"re-running the geofence-caps migration must be a no-op, not a duplicate-column error")
	for _, col := range []string{"geo_fence_vertex_ceiling", "geo_fence_ceiling", "geo_fence_vertex_budget"} {
		require.True(t, db.Migrator().HasColumn(&geoFenceCapsTenantRow{}, col))
	}
}

// TestTheGeoFenceCapsMigrationRollbackDropsOnlyItsOwnColumns pins WHICH columns the rollback
// drops. Nothing invokes gormigrate rollbacks today, which is exactly why it is worth a test
// rather than a reading: the day someone reaches for one, a rollback naming a neighbouring
// column would drop a tenant's shed priority or a governance ceiling, and it would do so while
// succeeding.
//
// It also pins that the rollback drops ALL THREE. A loop that stopped at the first column would
// leave two behind, and a later re-run of the chain would find them already present — a
// half-rolled-back schema that every existence check reports as fine.
func TestTheGeoFenceCapsMigrationRollbackDropsOnlyItsOwnColumns(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantGeoFenceCapsMigration().Rollback(db))

	for _, dropped := range []string{"geo_fence_vertex_ceiling", "geo_fence_ceiling", "geo_fence_vertex_budget"} {
		require.Falsef(t, db.Migrator().HasColumn(&geoFenceCapsTenantRow{}, dropped),
			"the rollback must drop %q, which this migration added", dropped)
	}
	for _, kept := range []string{
		"held_command_ceiling", "shed_priority", "ingest_messages_per_second", "outbound_burst",
		"basemap_tile_url", "token", "name",
	} {
		require.Truef(t, db.Migrator().HasColumn(&geoFenceCapsTenantRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
