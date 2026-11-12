// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAgentActuationGenerationMigration(t *testing.T) {
	db := newMigratedDB(t)
	m := NewAgentActuationGenerationMigration()
	require.NoError(t, m.Rollback(db))
	require.NoError(t, db.Exec(`INSERT INTO iam_agent_policies (tenant_id, created_at, updated_at) VALUES ('before', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, m.Migrate(db))
	require.NoError(t, db.Exec(`UPDATE iam_agent_policies SET actuation_generation = 7 WHERE tenant_id = 'before'`).Error)
	require.NoError(t, m.Migrate(db))
	var generation int
	require.NoError(t, db.Raw(`SELECT actuation_generation FROM iam_agent_policies WHERE tenant_id = 'before'`).Scan(&generation).Error)
	require.Equal(t, 7, generation)
	require.NoError(t, db.Exec(`INSERT INTO iam_agent_policies (tenant_id, created_at, updated_at) VALUES ('old-pod', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, db.Raw(`SELECT actuation_generation FROM iam_agent_policies WHERE tenant_id = 'old-pod'`).Scan(&generation).Error)
	require.Zero(t, generation)
}
