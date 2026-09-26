// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// tierColorRow is an independent read-back type, deliberately NOT either snapshot the
// migration writes through: sharing one would let a wrong TableName pass by being wrong in
// both places at once.
type tierColorRow struct {
	ID     uint
	Token  string
	Config string
	Color  *string
}

func (tierColorRow) TableName() string { return "iam_tenant_tiers" }

func countTiers(t *testing.T, db *gorm.DB, where string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM iam_tenant_tiers WHERE "+where).Scan(&n).Error)
	return n
}

// 🔴 THE ROWS, NOT ONLY THE DDL. migration-diff sees the column become nullable; it cannot
// see the backfill, because it compares schema only. The baseline seeds its tiers with the
// empty-string default, so after the full chain every one of them must hold NULL and none
// an empty string.
func TestTheTierColorMigrationConvertsEveryEmptyColorToNull(t *testing.T) {
	db := newMigratedDB(t)

	require.EqualValues(t, len(seededTiers), countTiers(t, db, "color IS NULL"),
		"every seeded tier has no pill, so every one must now hold NULL")
	require.Zero(t, countTiers(t, db, "color = ''"),
		"an empty-string colour survived the migration — the backfill did not run")
}

// The column accepts NULL, which is what "no pill" is now.
func TestTheTierColorColumnIsNullable(t *testing.T) {
	db := newMigratedDB(t)

	require.NoError(t, db.Create(&tierColorRow{Token: "no-pill", Config: "{}"}).Error)
	var back tierColorRow
	require.NoError(t, db.First(&back, "token = ?", "no-pill").Error)
	require.Nil(t, back.Color, "a tier inserted with no colour must read back NULL")

	amber := "amber"
	require.NoError(t, db.Create(&tierColorRow{Token: "amber-pill", Config: "{}", Color: &amber}).Error)
	var amberBack tierColorRow
	require.NoError(t, db.First(&amberBack, "token = ?", "amber-pill").Error)
	require.NotNil(t, amberBack.Color)
	require.Equal(t, "amber", *amberBack.Color)
}

// Re-runnable, as the chain requires: migrations run with UseTransaction:false and replay
// from the top after a failure. A second run is a no-op — and it still converts, which is
// what makes it safe against an empty string written by a pod on the previous release in between.
func TestTheTierColorMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec("INSERT INTO iam_tenant_tiers (token, color, display_order) VALUES ('late', '', 0)").Error)

	require.NoError(t, NewTierColorNullableMigration().Migrate(db),
		"re-running the tier colour migration must be a no-op, not an error")
	require.Zero(t, countTiers(t, db, "color = ''"))
	require.EqualValues(t, len(seededTiers)+1, countTiers(t, db, "color IS NULL"))
}

// 🔴 THE TABLE REBUILD MUST NOT COST THE TOKEN ITS UNIQUENESS. SQLite cannot alter a column
// in place, so the migration rebuilds the table there — and a rebuild drops separately
// created indexes. A tier table without its unique token index would make every SQLite
// test after this migration weaker than production.
func TestTheTierColorMigrationKeepsTheTierIndexes(t *testing.T) {
	db := newMigratedDB(t)

	for _, index := range []string{"idx_iam_tenant_tiers_token", "idx_iam_tenant_tiers_deleted_at"} {
		require.Truef(t, db.Migrator().HasIndex(&tierColorRow{}, index),
			"%s is missing after the chain — the table rebuild dropped it", index)
	}
	require.NoError(t, db.Create(&tierColorRow{Token: "dup", Config: "{}"}).Error)
	require.Error(t, db.Create(&tierColorRow{Token: "dup", Config: "{}"}).Error,
		"a second tier with the same token was accepted — the token is no longer unique")
}

// The rollback is implemented, and restores the shape before: every NULL colour back to
// empty strings, and the column NOT NULL with its empty-string default again.
func TestTheTierColorMigrationRollsBack(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTierColorNullableMigration().Rollback(db))

	require.Zero(t, countTiers(t, db, "color IS NULL"))
	require.EqualValues(t, len(seededTiers), countTiers(t, db, "color = ''"))

	require.NoError(t, db.Exec("INSERT INTO iam_tenant_tiers (token, display_order) VALUES ('defaulted', 0)").Error)
	var color string
	require.NoError(t, db.Raw("SELECT color FROM iam_tenant_tiers WHERE token = 'defaulted'").Scan(&color).Error)
	require.Equal(t, "", color, "an insert naming no colour must take the restored '' default")
	require.Error(t, db.Exec("INSERT INTO iam_tenant_tiers (token, color, display_order) VALUES ('nulled', NULL, 0)").Error,
		"the rolled-back column must refuse NULL again")
}

// The pinned TableName is what makes the migration touch the tier table at all.
func TestTheTierColorMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{"tier_color_nullable_snapshots", "tier_color_not_null_snapshots"} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the tier colour migration created %q instead of altering iam_tenant_tiers", stray)
	}
}
