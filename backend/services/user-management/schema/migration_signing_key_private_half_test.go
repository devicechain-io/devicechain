// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 migration-diff CANNOT SEE WHAT THIS MIGRATION IS FOR.
//
// It compares `pg_dump --schema-only`, so it sees the column drop and nothing else. The
// DELETE — the half that stops every key that was ever stored in cleartext from being
// trusted again — writes no DDL, and a migration that dropped the column but kept the
// rows would be green there while leaving the old keys' public halves in the JWKS. This
// file is the only guard on it, so it reads the rows back by value, with raw SQL that
// neither a soft-delete scope nor a snapshot type can bend.

// migrationIndex finds the chain position of the migration with the given ID.
func migrationIndex(t *testing.T, id string) int {
	t.Helper()
	for i, m := range Migrations {
		if m.ID == id {
			return i
		}
	}
	t.Fatalf("migration %s is not in the chain", id)
	return -1
}

// dbBeforeCleartextRemoval runs the chain up to, but not including, the private-half
// migration — the schema a live instance had when it upgraded.
func dbBeforeCleartextRemoval(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	stop := migrationIndex(t, NewSigningKeyPrivateHalfMigration().ID)
	for _, m := range Migrations[:stop] {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	return db
}

// signingKeyColumns lists signing_keys' columns as the database reports them.
func signingKeyColumns(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var cols []string
	require.NoError(t, db.Raw("SELECT name FROM pragma_table_info('signing_keys') ORDER BY cid").Scan(&cols).Error)
	require.NotEmpty(t, cols, "signing_keys must exist")
	return cols
}

// countSigningKeyRows counts every row, soft-deleted or not.
func countSigningKeyRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM signing_keys").Scan(&n).Error)
	return n
}

// An upgraded instance arrives with cleartext keys in the table: an active one, a
// retired one, and a soft-deleted one. All three hold a private key in cleartext, so
// all three must go — and the column with them.
func TestThePrivateHalfMigrationDeletesEveryCleartextKeyAndTheColumn(t *testing.T) {
	db := dbBeforeCleartextRemoval(t)

	// Seeded with raw SQL and literal values: the live model no longer has the column,
	// and a snapshot shared with the migration could be wrong in both places at once.
	require.NoError(t, db.Exec(`INSERT INTO signing_keys (created_at, updated_at, active, private_key_pem, public_key_pem)
		VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, true, '-----BEGIN PRIVATE KEY-----active', '-----BEGIN PUBLIC KEY-----active')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO signing_keys (created_at, updated_at, active, private_key_pem, public_key_pem, retired_at)
		VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, false, '-----BEGIN PRIVATE KEY-----retired', '-----BEGIN PUBLIC KEY-----retired', CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO signing_keys (created_at, updated_at, deleted_at, active, private_key_pem, public_key_pem)
		VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, false, '-----BEGIN PRIVATE KEY-----gone', '-----BEGIN PUBLIC KEY-----gone')`).Error)
	require.Equal(t, int64(3), countSigningKeyRows(t, db), "the seed is the premise")
	require.Contains(t, signingKeyColumns(t, db), "private_key_pem", "the seed is the premise")

	require.NoError(t, NewSigningKeyPrivateHalfMigration().Migrate(db))

	require.Equal(t, int64(0), countSigningKeyRows(t, db),
		"every key that was stored in cleartext must be deleted, soft-deleted rows included — "+
			"a surviving row keeps its public half in the JWKS, trusted with no end date")
	require.Equal(t,
		[]string{"id", "created_at", "updated_at", "deleted_at", "active", "public_key_pem", "retired_at"},
		signingKeyColumns(t, db),
		"private_key_pem must be gone, and nothing else may move")

	// Individually re-runnable: a replay finds no column and does nothing.
	require.NoError(t, NewSigningKeyPrivateHalfMigration().Migrate(db), "a replay must succeed")

	// And the table is writable with the public half only — the shape the service now
	// writes. A NOT NULL private_key_pem that survived would refuse this insert.
	require.NoError(t, db.Exec(`INSERT INTO signing_keys (created_at, updated_at, active, public_key_pem)
		VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, true, '-----BEGIN PUBLIC KEY-----new')`).Error)
	require.NoError(t, NewSigningKeyPrivateHalfMigration().Migrate(db), "a replay after the cutover must succeed")
	require.Equal(t, int64(1), countSigningKeyRows(t, db),
		"a replay after the column is gone must not touch a key written since the cutover")
}

// The whole chain, as a fresh install runs it: the secrets table the signing key is
// sealed into exists, and signing_keys has no column that could carry a private key.
func TestTheChainLeavesASecretsTableAndNoCleartextColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NotContains(t, signingKeyColumns(t, db), "private_key_pem")

	var secretCols []string
	require.NoError(t, db.Raw("SELECT name FROM pragma_table_info('secrets') ORDER BY cid").Scan(&secretCols).Error)
	require.Equal(t,
		[]string{"id", "created_at", "updated_at", "deleted_at", "tenant_id", "scope", "name",
			"ciphertext", "nonce", "wrapped_dek", "kek_version", "alg"},
		secretCols, "the secrets table must be core's, built by core's migration body")

	// The secrets table precedes the drop, so an instance never runs a chain in which
	// the cleartext is gone and there is nowhere to seal its replacement.
	require.Less(t,
		migrationIndex(t, "20260923130000"),
		migrationIndex(t, NewSigningKeyPrivateHalfMigration().ID))
}

// The rollback is not implemented, and says so rather than reporting success.
func TestThePrivateHalfMigrationRefusesRollback(t *testing.T) {
	db := newMigratedDB(t)
	require.Error(t, NewSigningKeyPrivateHalfMigration().Rollback(db))
}
