// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The agent-policy migration, run as the whole chain: the two tables, their uniqueness rules, the
// tenant column the purge sweeps by, and the service-account flag defaulting to false for an
// existing row (an account created before the flag is not an agent).
func TestTheAgentPolicyTablesAreCreated(t *testing.T) {
	db := newMigratedDB(t)
	for table, cols := range map[string][]string{
		"iam_agent_policies":        {"tenant_id", "draft", "draft_updated_at", "draft_updated_by", "published_version", "operator_suspended", "suspended_at", "suspended_by"},
		"iam_agent_policy_versions": {"tenant_id", "version", "document", "origin", "published_by"},
		"iam_service_accounts":      {"agent"},
	} {
		require.Truef(t, db.Migrator().HasTable(table), "%s was not created", table)
		for _, c := range cols {
			require.Truef(t, db.Migrator().HasColumn(table, c), "%s has no %s", table, c)
		}
	}
	for _, stray := range []string{"agent_policy_snapshots", "agent_policy_version_snapshots", "service_account_agent_snapshots"} {
		require.Falsef(t, db.Migrator().HasTable(stray), "a snapshot lost its TableName and created %s", stray)
	}

	head := func(tenant string) error {
		return db.Exec(`INSERT INTO iam_agent_policies (tenant_id, created_at, updated_at) VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, tenant).Error
	}
	require.NoError(t, head("acme"))
	require.Error(t, head("acme"), "one tenant held two policy heads")
	var pv int
	var susp bool
	require.NoError(t, db.Raw(`SELECT published_version, operator_suspended FROM iam_agent_policies WHERE tenant_id = 'acme'`).Row().Scan(&pv, &susp))
	require.Equal(t, 0, pv, "a new head has no published version")
	require.False(t, susp, "a new head is not suspended")

	ver := func(tenant string, v int) error {
		return db.Exec(`INSERT INTO iam_agent_policy_versions (tenant_id, version, document, origin, published_by, created_at) VALUES (?, ?, '{}', 'publish', 'a@x', CURRENT_TIMESTAMP)`, tenant, v).Error
	}
	require.NoError(t, ver("acme", 1))
	require.NoError(t, ver("globex", 1))
	require.Error(t, ver("acme", 1), "one tenant published one version twice")

	require.NoError(t, db.Exec(`INSERT INTO iam_service_accounts (tenant_id, token, client_id, enabled, created_at, updated_at) VALUES ('acme', 'ci', 'dcsa_1', true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	var agent bool
	require.NoError(t, db.Raw(`SELECT agent FROM iam_service_accounts WHERE client_id = 'dcsa_1'`).Row().Scan(&agent))
	require.False(t, agent, "an account written without the flag must not be an agent")
}
