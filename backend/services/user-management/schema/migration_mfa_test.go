// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Independent read-back types, deliberately NOT the snapshots the migration writes through, so a
// wrong pinned TableName cannot pass by being wrong in both places at once.
type mfaFactorRow struct {
	ID          uint
	IdentityID  uint
	Kind        string
	State       string
	SecretName  string
	LastStep    int64
	FailedCount int
	LockedUntil *time.Time
	CreatedAt   time.Time
}

func (mfaFactorRow) TableName() string { return "iam_mfa_factors" }

type mfaTenantRow struct {
	ID          uint
	Token       string
	Name        string
	TierID      uint
	MfaRequired bool
}

func (mfaTenantRow) TableName() string { return "iam_tenants" }

// foreignKeyDB is the migrated chain with foreign keys ENFORCED, which is what the cascade
// assertions need.
func foreignKeyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:mfa?mode=memory&cache=private&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	sqldb, err := db.DB()
	require.NoError(t, err)
	sqldb.SetMaxOpenConns(1)
	for _, m := range Migrations {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	var fk int
	require.NoError(t, db.Raw(`PRAGMA foreign_keys`).Scan(&fk).Error)
	require.Equal(t, 1, fk, "a test of foreign keys on a database not enforcing them proves nothing")
	return db
}

func TestTheMfaMigrationCreatesTheTablesAndNoStrayOnes(t *testing.T) {
	db := newMigratedDB(t)
	for _, table := range []string{"iam_mfa_factors", "iam_mfa_source_failures", "iam_mfa_recovery_codes"} {
		require.Truef(t, db.Migrator().HasTable(table), "%s must exist after the chain runs", table)
	}
	for _, stray := range []string{"mfa_factor_snapshots", "mfa_source_failure_snapshots",
		"mfa_recovery_code_snapshots", "mfa_tenant_snapshots", "mfa_identity_refs"} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the MFA migration created %q: a snapshot has lost its pinned TableName", stray)
	}
}

func TestTheMfaMigrationAddsTheTenantColumnAndNoStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	require.True(t, db.Migrator().HasColumn(&mfaTenantRow{}, "mfa_required"))
	require.NoError(t, db.Create(&mfaTenantRow{Token: "acme-mfa", Name: "Acme", TierID: seededTierID(t, db)}).Error)
	var back mfaTenantRow
	require.NoError(t, db.First(&back, "token = ?", "acme-mfa").Error)
	require.False(t, back.MfaRequired, "a tenant is not MFA-required until someone says so")
}

func TestTheMfaMigrationIsRerunnable(t *testing.T) {
	db := newMigratedDB(t)
	m := NewMfaMigration()
	require.NoError(t, m.Migrate(db), "a replay after a partial failure must converge")
	require.NoError(t, m.Migrate(db))
}

func TestTheMfaConstraintsHold(t *testing.T) {
	db := foreignKeyDB(t)
	require.NoError(t, db.Exec(`INSERT INTO iam_identities (id, email, enabled, password_hash, session_epoch, created_at, updated_at)
		VALUES (1, 'a@example.com', 1, '', 'e', datetime('now'), datetime('now'))`).Error)
	insert := func(kind, state string) error {
		return db.Create(&mfaFactorRow{IdentityID: 1, Kind: kind, State: state, SecretName: "mfa/totp/1", CreatedAt: time.Now()}).Error
	}
	require.NoError(t, insert("totp", "pending"))
	require.Error(t, insert("totp", "active"), "one factor of a kind per identity")
	require.Error(t, insert("webauthn", "pending"), "the kind is a closed vocabulary")
	require.Error(t, db.Exec(`UPDATE iam_mfa_factors SET state = 'confirmed'`).Error, "the state is a closed vocabulary")
	require.Error(t, db.Create(&mfaFactorRow{IdentityID: 999, Kind: "totp", State: "pending", SecretName: "x"}).Error,
		"a factor must name a real identity")

	var last int64
	require.NoError(t, db.Raw(`SELECT last_step FROM iam_mfa_factors WHERE identity_id = 1`).Scan(&last).Error)
	require.Zero(t, last, "last_step starts at zero so any real step is newer")

	// Recovery codes: unique per (identity, digest).
	require.NoError(t, db.Exec(`INSERT INTO iam_mfa_recovery_codes (identity_id, digest) VALUES (1, 'd')`).Error)
	require.Error(t, db.Exec(`INSERT INTO iam_mfa_recovery_codes (identity_id, digest) VALUES (1, 'd')`).Error)

	// Per-source rows: primary key (factor, source).
	require.NoError(t, db.Exec(`INSERT INTO iam_mfa_source_failures (factor_id, source_key, failed_count, updated_at)
		VALUES (1, '203.0.113.1', 1, datetime('now'))`).Error)
	require.Error(t, db.Exec(`INSERT INTO iam_mfa_source_failures (factor_id, source_key, failed_count, updated_at)
		VALUES (1, '203.0.113.1', 1, datetime('now'))`).Error)

	// Deleting the identity cascades every dependent row.
	require.NoError(t, db.Exec(`DELETE FROM iam_identities WHERE id = 1`).Error)
	for _, table := range []string{"iam_mfa_factors", "iam_mfa_recovery_codes", "iam_mfa_source_failures"} {
		var n int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM `+table).Scan(&n).Error)
		require.Zerof(t, n, "%s must cascade with the identity", table)
	}
}

// The migration seeds nothing, which is what keeps it inside the DDL-only bar that
// migration-diff's schema comparison can speak for.
func TestTheMfaMigrationSeedsNothing(t *testing.T) {
	db := newMigratedDB(t)
	for _, table := range []string{"iam_mfa_factors", "iam_mfa_recovery_codes", "iam_mfa_source_failures"} {
		var n int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM `+table).Scan(&n).Error)
		require.Zerof(t, n, "%s must start empty", table)
	}
}
