// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheProviderSignOutMigrationAddsTheColumnOffByDefault runs the whole chain and pins
// the one thing the column means: a connection signs people out at its provider only when
// an administrator said so. A connection inserted without naming the column reads back
// false — which is also every connection that existed before this migration.
func TestTheProviderSignOutMigrationAddsTheColumnOffByDefault(t *testing.T) {
	db := newFKMigratedDB(t)

	require.True(t, db.Migrator().HasColumn("iam_idp_connections", "provider_sign_out"))
	require.NoError(t, db.Exec(`INSERT INTO iam_idp_connections
		(token, name, kind, issuer, client_id, created_at, updated_at)
		VALUES ('c1', 'C1', 'tenant', 'https://idp.example', 'cid', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	var on bool
	require.NoError(t, db.Raw(`SELECT provider_sign_out FROM iam_idp_connections WHERE token='c1'`).Row().Scan(&on))
	require.False(t, on, "a connection must not sign people out at its provider unless switched on")
}

// The control for the pinned TableName: without it AutoMigrate creates a table named
// after the snapshot type, succeeds, and leaves the connections table without the column.
func TestTheProviderSignOutMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{"sso_provider_sign_out_snapshots", "sso_provider_sign_outs"} {
		require.Falsef(t, db.Migrator().HasTable(stray), "stray table %q: the snapshot lost its TableName", stray)
	}
}

// Migrations replay from the top after a failure, so a second run must be a no-op.
func TestTheProviderSignOutMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewSsoProviderSignOutMigration().Migrate(db))
	require.True(t, db.Migrator().HasColumn("iam_idp_connections", "provider_sign_out"))
}

// The rollback drops its own column and nothing beside it.
func TestTheProviderSignOutMigrationRollbackDropsOnlyItsOwnColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewSsoProviderSignOutMigration().Rollback(db))
	require.False(t, db.Migrator().HasColumn("iam_idp_connections", "provider_sign_out"))
	for _, kept := range []string{"session_max_age_seconds", "discovery_snapshot", "enabled", "token"} {
		require.Truef(t, db.Migrator().HasColumn("iam_idp_connections", kept), "the rollback dropped %q", kept)
	}
}
