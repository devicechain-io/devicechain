// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The same reasoning as migration_tenant_basemap_test.go: migration-diff gates the
// Postgres schema in CI, but the one mistake this migration is most likely to make —
// losing the pinned TableName — does not FAIL there in a way anyone would notice
// locally. AutoMigrate would create a `tenant_held_command_ceiling_snapshots` table,
// report success, and leave iam_tenants without the column every reader and writer now
// assigns to.
//
// heldCeilingTenantRow is an independent read-back type, deliberately NOT the snapshot
// the migration writes through. Sharing the struct would let a wrong TableName pass by
// being wrong in both places at once.
type heldCeilingTenantRow struct {
	ID    uint
	Token string
	Name  string
	// A tenant's tier is NOT NULL (ADR-065), so every insert here has to name one.
	TierID             uint
	HeldCommandCeiling *int
}

func (heldCeilingTenantRow) TableName() string { return "iam_tenants" }

// TestTheHeldCommandCeilingMigrationAddsTheColumnToTheTenantTable runs the whole chain
// and proves the column landed on the tenant table — by writing through it and reading
// it back, not by asking the migrator whether it thinks it exists.
//
// It also pins the column NULLABLE, which is what makes "inherit the tier below"
// expressible: a NOT NULL column defaulting to 0 would read as "this tenant may hold
// nothing", refusing every command to an absent device, and it would do so for every
// tenant that never configured one.
func TestTheHeldCommandCeilingMigrationAddsTheColumnToTheTenantTable(t *testing.T) {
	db := newMigratedDB(t)

	require.True(t, db.Migrator().HasColumn(&heldCeilingTenantRow{}, "held_command_ceiling"),
		"held_command_ceiling must exist on iam_tenants after the chain runs")

	tierID := seededTierID(t, db)
	ceiling := 2500
	require.NoError(t, db.Create(&heldCeilingTenantRow{
		Token: "bounded", Name: "Bounded", TierID: tierID, HeldCommandCeiling: &ceiling,
	}).Error)

	var back heldCeilingTenantRow
	require.NoError(t, db.First(&back, "token = ?", "bounded").Error)
	require.NotNil(t, back.HeldCommandCeiling)
	require.Equal(t, 2500, *back.HeldCommandCeiling)

	// A tenant that declares none reads back NULL — inherit — not zero. The distinction
	// is the whole cascade: zero would be a bound of "none permitted", which is the one
	// reading of an unconfigured tenant that is an outage rather than a default.
	require.NoError(t, db.Create(&heldCeilingTenantRow{
		Token: "inherits", Name: "Inherits", TierID: tierID,
	}).Error)
	var inherits heldCeilingTenantRow
	require.NoError(t, db.First(&inherits, "token = ?", "inherits").Error)
	require.Nil(t, inherits.HeldCommandCeiling,
		"an unset ceiling must read back as NULL (inherit), not as 0 (hold nothing)")
}

// TestTheHeldCommandCeilingMigrationDoesNotCreateAStrayTable is the control, and it is
// the one that catches the mistake this migration is actually exposed to. Without the
// pinned TableName, AutoMigrate derives the table from the Go type name and creates a
// brand-new table — succeeding, and leaving the tenant row untouched.
func TestTheHeldCommandCeilingMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, stray := range []string{
		"tenant_held_command_ceiling_snapshots", "tenant_held_command_ceilings",
	} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the held-command-ceiling migration created %q instead of altering iam_tenants — its "+
				"snapshot has lost its pinned TableName, so it migrated nothing", stray)
	}
}

// TestTheHeldCommandCeilingMigrationIsReRunnable pins what the chain requires of every
// appended migration: migrations run with UseTransaction:false and replay from the top
// after a failure, so one that is not individually re-runnable turns a single transient
// failure into a stuck instance.
func TestTheHeldCommandCeilingMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantHeldCommandCeilingMigration().Migrate(db),
		"re-running the held-command-ceiling migration must be a no-op, not a duplicate-column error")
	require.True(t, db.Migrator().HasColumn(&heldCeilingTenantRow{}, "held_command_ceiling"))
}

// TestTheHeldCommandCeilingMigrationRollbackDropsOnlyItsOwnColumn pins WHICH column the
// rollback drops. Nothing invokes gormigrate rollbacks today, which is exactly why it is
// worth a test rather than a reading: the day someone reaches for one, a rollback naming
// a neighbouring column would drop a tenant's shed priority or a governance ceiling, and
// it would do so while succeeding.
func TestTheHeldCommandCeilingMigrationRollbackDropsOnlyItsOwnColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantHeldCommandCeilingMigration().Rollback(db))

	require.False(t, db.Migrator().HasColumn(&heldCeilingTenantRow{}, "held_command_ceiling"),
		"the rollback must drop held_command_ceiling, which this migration added")
	for _, kept := range []string{
		"shed_priority", "ingest_messages_per_second", "outbound_burst",
		"basemap_tile_url", "token", "name",
	} {
		require.Truef(t, db.Migrator().HasColumn(&heldCeilingTenantRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
