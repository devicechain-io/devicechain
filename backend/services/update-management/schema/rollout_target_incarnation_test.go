// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRolloutTargetIncarnationMigrationIsNullableRerunnableAndLast(t *testing.T) {
	require.Equal(t, "20261118000100", RolloutTargetIncarnationID)
	require.Equal(t, RolloutTargetIncarnationID, Migrations[len(Migrations)-4].ID)
	for _, m := range Migrations[:len(Migrations)-4] {
		require.Greater(t, RolloutTargetIncarnationID, m.ID)
	}
	db := openDB(t)
	require.NoError(t, NewRolloutsSchema().Migrate(db))
	// Existing targets survive the addition as NULL; no identity is guessed or backfilled.
	oldInsert := `INSERT INTO update_rollout_targets
	 (tenant_id, rollout_id, device_token, status, skip_code, skip_detail) VALUES ('acme', 1, ?, 'PENDING', '', '')`
	require.NoError(t, db.Exec(oldInsert, "before").Error)
	for range 3 {
		require.NoError(t, NewRolloutTargetIncarnationSchema().Migrate(db))
	}
	cols, err := db.Migrator().ColumnTypes("update_rollout_targets")
	require.NoError(t, err)
	found := false
	for _, c := range cols {
		if c.Name() == "device_incarnation" {
			found = true
			nullable, ok := c.Nullable()
			require.True(t, ok)
			require.True(t, nullable)
			require.Equal(t, "bigint", c.DatabaseTypeName())
		}
	}
	require.True(t, found)
	// Old v1.7.0 pods keep inserting without the new column during the overlap.
	require.NoError(t, db.Exec(oldInsert, "during").Error)
	var count int64
	require.NoError(t, db.Table("update_rollout_targets").Where("device_incarnation IS NULL").Count(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Exec(`UPDATE update_rollout_targets SET device_incarnation = 9223372036854775807 WHERE device_token = 'during'`).Error)
	var id int64
	require.NoError(t, db.Table("update_rollout_targets").Select("device_incarnation").Where("device_token = 'during'").Scan(&id).Error)
	require.Equal(t, int64(9223372036854775807), id)
}
