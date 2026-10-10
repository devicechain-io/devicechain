// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rateKeyTenantRow is an independent read-back type for iam_tenants, deliberately NOT the
// migration's snapshot: sharing one would let a wrong TableName pass by being wrong in
// both places at once.
type rateKeyTenantRow struct {
	ID uint
}

func (rateKeyTenantRow) TableName() string { return "iam_tenants" }

func tierConfig(t *testing.T, db *gorm.DB, token string) string {
	t.Helper()
	var cfg string
	require.NoError(t, db.Raw("SELECT config FROM iam_tenant_tiers WHERE token = ?", token).Scan(&cfg).Error)
	return cfg
}

// The columns are renamed, not added beside the old ones: after the chain each new name
// exists and each old one does not, and no stray snapshot table was created.
func TestTheRateKeyMigrationRenamesTheTenantColumns(t *testing.T) {
	db := newMigratedDB(t)
	for _, r := range rateKeyRenames {
		require.Truef(t, db.Migrator().HasColumn(&rateKeyTenantRow{}, r.newColumn), "%s is missing", r.newColumn)
		require.Falsef(t, db.Migrator().HasColumn(&rateKeyTenantRow{}, r.oldColumn), "%s survived the rename", r.oldColumn)
	}
	require.False(t, db.Migrator().HasTable("rate_key_tenant_snapshots"))
}

// An override set under the old column survives the rename at the same value — the rename
// must move the data, not drop the column and add an empty one.
func TestTheRateKeyMigrationKeepsTenantOverrides(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewRateKeysNameUnitsMigration().Rollback(db))
	require.NoError(t, db.Exec(
		"INSERT INTO iam_tenants (token, tier_id, ingest_messages_per_second, outbound_messages_per_second) "+
			"VALUES ('acme', (SELECT id FROM iam_tenant_tiers WHERE token = 'gold'), 1234.5, 7)").Error)

	require.NoError(t, NewRateKeysNameUnitsMigration().Migrate(db))

	var got struct{ Ingest, Outbound float64 }
	require.NoError(t, db.Raw("SELECT ingest_readings_per_second AS ingest, outbound_calls_per_second AS outbound "+
		"FROM iam_tenants WHERE token = 'acme'").Scan(&got).Error)
	require.Equal(t, 1234.5, got.Ingest)
	require.Equal(t, float64(7), got.Outbound)
}

// Re-runnable, as the chain requires — and a re-run still re-keys, which is what makes it
// safe against a tier written under the old key by a pod on the previous release between
// two runs. Keys it does not rename, and their numbers, pass through exactly.
func TestTheRateKeyMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec("INSERT INTO iam_tenant_tiers (token, config, display_order) VALUES "+
		`('late', '{"ingestMessagesPerSecond":0.5,"outboundMessagesPerSecond":12,"shedPriority":40}', 0)`).Error)

	require.NoError(t, NewRateKeysNameUnitsMigration().Migrate(db), "a re-run must be a no-op, not an error")
	require.JSONEq(t, `{"ingestReadingsPerSecond":0.5,"outboundCallsPerSecond":12,"shedPriority":40}`, tierConfig(t, db, "late"))

	before := tierConfig(t, db, "gold")
	require.NoError(t, NewRateKeysNameUnitsMigration().Migrate(db))
	require.Equal(t, before, tierConfig(t, db, "gold"), "an already re-keyed tier must not be rewritten")
}

// A tier carrying BOTH spellings of one ceiling is refused, not resolved: neither release
// can write it, and choosing a winner would discard a ceiling someone set by hand.
func TestTheRateKeyMigrationRefusesATierCarryingBothKeys(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec("INSERT INTO iam_tenant_tiers (token, config, display_order) VALUES "+
		`('both', '{"ingestMessagesPerSecond":1,"ingestReadingsPerSecond":2}', 0)`).Error)

	err := NewRateKeysNameUnitsMigration().Migrate(db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ingestReadingsPerSecond")
	require.JSONEq(t, `{"ingestMessagesPerSecond":1,"ingestReadingsPerSecond":2}`, tierConfig(t, db, "both"),
		"a refused row must be left exactly as it was")
}

// The rollback restores the shape before: the old column names and the old keys, at the
// same values.
func TestTheRateKeyMigrationRollsBack(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewRateKeysNameUnitsMigration().Rollback(db))

	for _, r := range rateKeyRenames {
		require.True(t, db.Migrator().HasColumn(&rateKeyTenantRow{}, r.oldColumn))
		require.False(t, db.Migrator().HasColumn(&rateKeyTenantRow{}, r.newColumn))
	}
	require.JSONEq(t,
		`{"ingestMessagesPerSecond":2000,"ingestBurst":4000,"outboundMessagesPerSecond":200,"outboundBurst":400,"shedPriority":90}`,
		tierConfig(t, db, "gold"))
}
