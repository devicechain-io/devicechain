// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRolloutSafetyMigrationReplaysAndAllowsOldWriters(t *testing.T) {
	db := openDB(t)
	require.Equal(t, "20261121000100", RolloutSafetyID)
	require.Equal(t, RolloutSafetyID, Migrations[len(Migrations)-3].ID)
	require.NoError(t, NewRolloutsSchema().Migrate(db))
	insert := `INSERT INTO update_rollouts (tenant_id, token, name, artifact_id, group_token, max_in_flight, state, pause_reason, target_count)
	 VALUES ('acme', ?, 'rollout', 1, 'fleet', 1, 'DRAFT', '', 0)`
	require.NoError(t, db.Exec(insert, "before").Error)
	for range 3 {
		require.NoError(t, NewRolloutSafetySchema().Migrate(db))
	}
	require.NoError(t, db.Exec(insert, "during").Error)
	var count int64
	require.NoError(t, db.Table("update_rollouts").Where("cohort_plan IS NULL AND failure_rate_plan IS NULL AND current_cohort = 0 AND halt_sequence = 0 AND halt_failures = 0 AND halt_samples = 0 AND rate_override_at IS NULL").Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, db.Exec(`INSERT INTO update_rollout_targets (tenant_id, rollout_id, device_token, status, skip_code, skip_detail) VALUES ('acme', 1, 'd', 'PENDING', '', '')`).Error)
	require.NoError(t, db.Table("update_rollout_targets").Where("cohort = 0").Count(&count).Error)
	require.EqualValues(t, 1, count)
}
