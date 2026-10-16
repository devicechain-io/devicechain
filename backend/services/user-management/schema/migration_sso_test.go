// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newFKMigratedDB runs the whole chain on a SQLite database that ENFORCES foreign keys.
// The pragma rides the DSN, not a statement: gorm pools connections, and a pragma issued
// as a statement configures only the one that happened to serve it.
func newFKMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil, "_pragma=foreign_keys(1)")
	for _, m := range Migrations {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	return db
}

// TestTheSsoMigrationCreatesExactlyItsTables is the control for the pinned TableNames:
// without them AutoMigrate derives names from the Go types and succeeds on tables nothing
// reads, leaving the real ones absent — and leaving iam_memberships / iam_tenants without
// the two columns every later writer assigns to.
func TestTheSsoMigrationCreatesExactlyItsTables(t *testing.T) {
	db := newMigratedDB(t)

	for _, table := range []string{
		"iam_idp_connections", "iam_idp_connection_tenants", "iam_idp_connection_domains",
		"iam_idp_role_mappings", "iam_identity_links", "iam_sso_link_requests",
	} {
		require.Truef(t, db.Migrator().HasTable(table), "%s must exist after the chain runs", table)
	}
	for _, stray := range []string{
		"idp_connections", "idp_connection_tenants", "idp_connection_domains", "idp_role_mappings",
		"identity_links", "sso_link_requests", "sso_identity_refs", "sso_role_refs",
		"sso_membership_snapshots", "sso_tenant_snapshots",
	} {
		require.Falsef(t, db.Migrator().HasTable(stray), "stray table %q: a TableName is missing", stray)
	}

	require.True(t, db.Migrator().HasColumn("iam_memberships", "managed_by"))
	require.True(t, db.Migrator().HasColumn("iam_tenants", "sso_enforced"))
	require.True(t, db.Migrator().HasColumn("iam_idp_connections", "session_max_age_seconds"))
}

// TestTheSsoMigrationIsReRunnable: migrations replay from the top after a failure.
func TestTheSsoMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewSsoConnectionsMigration().Migrate(db), "a second run must be a no-op")
}

// TestSessionCeilingAndEnforcementDefaults pins the two column defaults that mean
// something: a connection's ceiling is 12 h unless set, and a tenant does not require
// SSO unless an operator says so.
func TestSessionCeilingAndEnforcementDefaults(t *testing.T) {
	db := newFKMigratedDB(t)

	require.NoError(t, db.Exec(`INSERT INTO iam_idp_connections
		(token, name, kind, issuer, client_id, created_at, updated_at)
		VALUES ('c1', 'C1', 'tenant', 'https://idp.example', 'cid', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	var ceiling int
	var enabled bool
	require.NoError(t, db.Raw(`SELECT session_max_age_seconds, enabled FROM iam_idp_connections WHERE token='c1'`).
		Row().Scan(&ceiling, &enabled))
	require.Equal(t, 43200, ceiling)
	require.False(t, enabled, "a connection is created disabled unless it says otherwise")

	tierID := seededTierID(t, db)
	require.NoError(t, db.Exec(`INSERT INTO iam_tenants (token, tier_id, created_at, updated_at)
		VALUES ('acme', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, tierID).Error)
	var enforced bool
	require.NoError(t, db.Raw(`SELECT sso_enforced FROM iam_tenants WHERE token='acme'`).Row().Scan(&enforced))
	require.False(t, enforced)
}
