// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A connection inserted without naming the column reads back empty, which is also every
// connection that existed before this migration: system roots only.
func TestTheCaBundleMigrationAddsTheColumnEmptyByDefault(t *testing.T) {
	db := newFKMigratedDB(t)

	require.True(t, db.Migrator().HasColumn("iam_idp_connections", "ca_bundle"))
	require.NoError(t, db.Exec(`INSERT INTO iam_idp_connections
		(token, name, kind, issuer, client_id, created_at, updated_at)
		VALUES ('c1', 'C1', 'tenant', 'https://idp.example', 'cid', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	var pem string
	require.NoError(t, db.Raw(`SELECT ca_bundle FROM iam_idp_connections WHERE token='c1'`).Row().Scan(&pem))
	require.Empty(t, pem)
}

func TestTheCaBundleMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{"sso_ca_bundle_snapshots", "sso_ca_bundles"} {
		require.Falsef(t, db.Migrator().HasTable(stray), "stray table %q: the snapshot lost its TableName", stray)
	}
}

func TestTheCaBundleMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewSsoCaBundleMigration().Migrate(db))
	require.True(t, db.Migrator().HasColumn("iam_idp_connections", "ca_bundle"))
}

func TestTheCaBundleMigrationRollbackDropsOnlyItsOwnColumn(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewSsoCaBundleMigration().Rollback(db))
	require.False(t, db.Migrator().HasColumn("iam_idp_connections", "ca_bundle"))
	for _, kept := range []string{"provider_sign_out", "discovery_snapshot", "token"} {
		require.Truef(t, db.Migrator().HasColumn("iam_idp_connections", kept), "the rollback dropped %q", kept)
	}
}
