// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 THE ROWS. This migration changes no DDL, so migration-diff cannot see it at all; only
// a test that seeds the empty strings an earlier release wrote and reads them back can.
func TestTheIdentityNamesMigrationConvertsEmptyNamesToNull(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec(`INSERT INTO iam_identities (email, first_name, last_name, enabled, password_hash)
		VALUES ('blank@example.invalid', '', '', true, 'h'),
		       ('named@example.invalid', 'Ada', '', true, 'h'),
		       ('null@example.invalid', NULL, NULL, true, 'h')`).Error)

	require.NoError(t, NewIdentityNamesNullMigration().Migrate(db))

	type names struct {
		FirstName *string
		LastName  *string
	}
	read := func(email string) names {
		var n names
		require.NoError(t, db.Raw("SELECT first_name, last_name FROM iam_identities WHERE email = ?", email).Scan(&n).Error)
		return n
	}
	blank := read("blank@example.invalid")
	require.Nil(t, blank.FirstName, "an empty first name must be NULL after the migration")
	require.Nil(t, blank.LastName, "an empty last name must be NULL after the migration")

	named := read("named@example.invalid")
	require.NotNil(t, named.FirstName)
	require.Equal(t, "Ada", *named.FirstName, "a real name must not be touched")
	require.Nil(t, named.LastName)

	already := read("null@example.invalid")
	require.Nil(t, already.FirstName)
	require.Nil(t, already.LastName)

	// Re-runnable, as the chain requires.
	require.NoError(t, NewIdentityNamesNullMigration().Migrate(db))
	require.Equal(t, "Ada", *read("named@example.invalid").FirstName)
}

// Unimplemented must fail loudly: the rollback refuses rather than inventing empty strings.
func TestTheIdentityNamesMigrationRefusesRollback(t *testing.T) {
	db := newMigratedDB(t)
	require.Error(t, NewIdentityNamesNullMigration().Rollback(db))
}
