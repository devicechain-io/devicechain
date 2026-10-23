// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The service-accounts migration, run as the whole chain and checked by writing through the
// tables. migration-diff gates the Postgres DDL; this pins what a DDL diff cannot show: the
// uniqueness rules, and that the tenant column the purge sweeps by is there.
func TestTheServiceAccountTablesAreCreated(t *testing.T) {
	db := newMigratedDB(t)
	for table, cols := range map[string][]string{
		"iam_service_accounts":        {"tenant_id", "token", "client_id", "enabled", "scope", "created_by", "name", "description"},
		"iam_service_account_roles":   {"service_account_id", "role_id"},
		"iam_service_account_secrets": {"service_account_id", "digest", "hint", "expires_at", "last_used_at"},
	} {
		require.Truef(t, db.Migrator().HasTable(table), "%s was not created", table)
		for _, c := range cols {
			require.Truef(t, db.Migrator().HasColumn(table, c), "%s has no %s", table, c)
		}
	}
	for _, stray := range []string{"service_account_snapshots", "service_account_role_snapshots", "service_account_secret_snapshots"} {
		require.Falsef(t, db.Migrator().HasTable(stray), "a snapshot lost its TableName and created %s", stray)
	}

	ins := func(tenant, token, client string) error {
		return db.Exec(`INSERT INTO iam_service_accounts (tenant_id, token, client_id, enabled, created_at, updated_at) VALUES (?, ?, ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			tenant, token, client).Error
	}
	require.NoError(t, ins("acme", "ci", "dcsa_1"))
	require.NoError(t, ins("globex", "ci", "dcsa_2"), "two tenants may each have an account at one token")
	require.Error(t, ins("acme", "ci", "dcsa_3"), "one tenant held two accounts at one token")
	require.Error(t, ins("initech", "other", "dcsa_1"), "two accounts share one client id")

	var scope *string
	require.NoError(t, db.Raw(`SELECT scope FROM iam_service_accounts WHERE client_id = 'dcsa_1'`).Scan(&scope).Error)
	require.Nil(t, scope, "an account with no scope must read as the whole tenant")
}
