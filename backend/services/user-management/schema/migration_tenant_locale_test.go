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
// locally. AutoMigrate would create a `tenant_locale_snapshots` table, report success,
// and leave iam_tenants without the column every reader and writer now assigns to.
//
// localeTenantRow is an independent read-back type, deliberately NOT the snapshot the
// migration writes through. Sharing the struct would let a wrong TableName pass by
// being wrong in both places at once.
type localeTenantRow struct {
	ID    uint
	Token string
	Name  string
	// A tenant's tier is NOT NULL (ADR-065), so every insert here has to name one.
	TierID uint
	Locale *string
}

func (localeTenantRow) TableName() string { return "iam_tenants" }

// TestTheLocaleMigrationAddsTheColumnToTheTenantTable runs the whole chain and proves
// the column landed on the tenant table — by writing through it and reading it back,
// not by asking the migrator whether it thinks it exists.
func TestTheLocaleMigrationAddsTheColumnToTheTenantTable(t *testing.T) {
	db := newMigratedDB(t)

	require.True(t, db.Migrator().HasColumn(&localeTenantRow{}, "locale"),
		"locale must exist on iam_tenants after the chain runs")

	tag := "es-MX"
	require.NoError(t, db.Create(&localeTenantRow{
		Token: "acme-locale", Name: "Acme", TierID: seededTierID(t, db), Locale: &tag,
	}).Error)

	var back localeTenantRow
	require.NoError(t, db.First(&back, "token = ?", "acme-locale").Error)
	require.NotNil(t, back.Locale)
	// The full canonical tag round-trips. A column narrower than a region-qualified
	// tag would truncate "es-MX" to "es" and silently move a tenant's default.
	require.Equal(t, "es-MX", *back.Locale)
}

// The column is NULLABLE, which is what makes "inherit the tier below" expressible at
// all. A NOT NULL column with an empty-string default would turn every un-configured
// tenant into one that has explicitly chosen a blank locale — which, absent the
// normalization in the locale package, masks the operator's `locale.default`.
func TestTheLocaleColumnIsNullableSoATenantCanInherit(t *testing.T) {
	db := newMigratedDB(t)

	require.NoError(t, db.Create(&localeTenantRow{
		Token: "inherits-locale", Name: "Inherits", TierID: seededTierID(t, db),
	}).Error)

	var back localeTenantRow
	require.NoError(t, db.First(&back, "token = ?", "inherits-locale").Error)
	require.Nil(t, back.Locale, "an unset locale must read back as NULL, not as an empty string")
}

// TestTheLocaleMigrationDoesNotCreateAStrayTable is the control, and it is the one that
// catches the mistake this migration is actually exposed to. Without the pinned
// TableName, AutoMigrate derives the table from the Go type name and creates a brand-new
// table — succeeding, and leaving the tenant row untouched.
func TestTheLocaleMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, stray := range []string{"tenant_locale_snapshots", "tenant_locales"} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the locale migration created %q instead of altering iam_tenants — its snapshot has "+
				"lost its pinned TableName, so it migrated nothing", stray)
	}
}

// TestTheLocaleMigrationIsReRunnable pins what the chain requires of every appended
// migration: migrations run with UseTransaction:false and replay from the top after a
// failure, so one that is not individually re-runnable turns a single transient failure
// into a stuck instance.
func TestTheLocaleMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantLocaleMigration().Migrate(db),
		"re-running the locale migration must be a no-op, not a duplicate-column error")
	require.True(t, db.Migrator().HasColumn(&localeTenantRow{}, "locale"))
}

// TestTheLocaleMigrationRollbackDropsOnlyItsOwnColumn pins WHICH column the rollback
// drops. Nothing invokes gormigrate rollbacks today, which is exactly why it is worth a
// test rather than a reading: the day someone reaches for one, a rollback naming a
// neighbouring column would drop a tenant's branding or its governance ceiling, and it
// would do so while succeeding.
func TestTheLocaleMigrationRollbackDropsOnlyItsOwnColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantLocaleMigration().Rollback(db))

	require.False(t, db.Migrator().HasColumn(&localeTenantRow{}, "locale"),
		"the rollback must drop the column this migration added")
	for _, kept := range []string{
		"branding_title", "branding_primary", "basemap_tile_url",
		"ingest_messages_per_second", "outbound_burst", "token", "name",
	} {
		require.Truef(t, db.Migrator().HasColumn(&localeTenantRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
