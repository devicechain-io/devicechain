// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentActuationOffMigration(t *testing.T) {
	db := newMigratedDB(t)
	migration := NewAgentActuationOffMigration()
	require.NoError(t, migration.Rollback(db))
	insert := func(tenant string) {
		require.NoError(t, db.Exec(`INSERT INTO iam_agent_policies (tenant_id, created_at, updated_at) VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, tenant).Error)
	}
	insert("before")
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, migration.Migrate(db), "replay must succeed")
	insert("old-pod")
	for _, tenant := range []string{"before", "old-pod"} {
		var off bool
		require.NoError(t, db.Raw(`SELECT tenant_actuation_off FROM iam_agent_policies WHERE tenant_id = ?`, tenant).Row().Scan(&off))
		require.False(t, off)
	}
	for _, col := range []string{"tenant_actuation_off", "actuation_off_at", "actuation_off_by"} {
		require.True(t, db.Migrator().HasColumn("iam_agent_policies", col))
	}
	require.False(t, db.Migrator().HasTable("agent_actuation_off_snapshots"))
}
