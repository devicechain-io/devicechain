// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// sessionEpochIdentityRow is an independent read-back type, deliberately NOT the
// snapshot the migration writes through. Sharing the struct would let a wrong
// TableName pass by being wrong in both places at once.
type sessionEpochIdentityRow struct {
	ID           uint
	Email        string
	PasswordHash string
	SessionEpoch string
}

func (sessionEpochIdentityRow) TableName() string { return "iam_identities" }

// migratedUpTo runs every migration BEFORE the session-epoch one, so a test can
// plant rows the way an existing instance holds them when the upgrade arrives.
func migratedUpTo(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	target := NewIdentitySessionEpochMigration().ID
	for _, m := range Migrations {
		if m.ID == target {
			return db
		}
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	t.Fatalf("the session-epoch migration %s is not in the chain", target)
	return nil
}

func epochOf(t *testing.T, db *gorm.DB, email string) string {
	t.Helper()
	var row sessionEpochIdentityRow
	require.NoError(t, db.First(&row, "email = ?", email).Error)
	return row.SessionEpoch
}

// The column lands on iam_identities — written through and read back, not asked of
// the migrator — and the chain's own default for an insert that omits it is the empty
// string.
func TestTheSessionEpochMigrationAddsTheColumnToTheIdentityTable(t *testing.T) {
	db := newMigratedDB(t)
	require.True(t, db.Migrator().HasColumn(&sessionEpochIdentityRow{}, "session_epoch"))

	require.NoError(t, db.Create(&sessionEpochIdentityRow{
		Email: "a@example.com", PasswordHash: "h", SessionEpoch: "e-1",
	}).Error)
	require.Equal(t, "e-1", epochOf(t, db, "a@example.com"))

	// An INSERT that omits the column — an old pod during a rolling upgrade — must
	// still succeed, and land on the empty default the mint refuses.
	require.NoError(t, db.Exec(
		`INSERT INTO iam_identities (email, password_hash, enabled) VALUES (?, ?, ?)`,
		"old-pod@example.com", "h", true).Error)
	require.Equal(t, "", epochOf(t, db, "old-pod@example.com"))
}

func TestTheSessionEpochMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{"identity_session_epoch_snapshots", "identity_session_epochs"} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the session-epoch migration created %q instead of altering iam_identities — its "+
				"snapshot has lost its pinned TableName, so it migrated nothing", stray)
	}
}

// Identities that exist when the upgrade runs get an epoch, and each its OWN: a shared
// value would mean one person's password reset leaves nobody else's session alone
// only by accident of every other row also rotating — and, worse, a token minted for
// one identity would match the epoch of another.
func TestTheSessionEpochMigrationBackfillsADistinctValuePerExistingIdentity(t *testing.T) {
	db := migratedUpTo(t)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		require.NoError(t, db.Exec(
			`INSERT INTO iam_identities (email, password_hash, enabled) VALUES (?, ?, ?)`,
			email, "h", true).Error)
	}

	require.NoError(t, NewIdentitySessionEpochMigration().Migrate(db))

	a, b := epochOf(t, db, "a@example.com"), epochOf(t, db, "b@example.com")
	require.NotEmpty(t, a, "an existing identity was left with no session epoch")
	require.NotEmpty(t, b)
	require.NotEqual(t, a, b, "two existing identities were given the same session epoch")
	require.Len(t, a, 22, "128 bits, base64url without padding")
}

// Re-running (the chain replays from the top after a failure) must neither rewrite
// nor rotate a value already set — rotating would sign everyone out on every replay —
// while still filling a row that arrived empty in between.
func TestTheSessionEpochMigrationIsReRunnableAndLeavesSetValuesAlone(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Create(&sessionEpochIdentityRow{
		Email: "set@example.com", PasswordHash: "h", SessionEpoch: "already-set",
	}).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO iam_identities (email, password_hash, enabled) VALUES (?, ?, ?)`,
		"blank@example.com", "h", true).Error)

	require.NoError(t, NewIdentitySessionEpochMigration().Migrate(db))
	require.Equal(t, "already-set", epochOf(t, db, "set@example.com"),
		"a re-run rewrote an epoch that was already set")
	filled := epochOf(t, db, "blank@example.com")
	require.NotEmpty(t, filled)

	require.NoError(t, NewIdentitySessionEpochMigration().Migrate(db))
	require.Equal(t, filled, epochOf(t, db, "blank@example.com"),
		"a second re-run rotated an epoch the first one set")
}

func TestTheSessionEpochMigrationRollbackDropsOnlyItsOwnColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewIdentitySessionEpochMigration().Rollback(db))
	require.False(t, db.Migrator().HasColumn(&sessionEpochIdentityRow{}, "session_epoch"))
	for _, kept := range []string{"email", "password_hash", "enabled", "first_name"} {
		require.Truef(t, db.Migrator().HasColumn(&sessionEpochIdentityRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
