// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttemptUpdatedObservedAtMigrationIsNullableRerunnableAndLast(t *testing.T) {
	require.Equal(t, "20261128000100", AttemptUpdatedObservedAtID)
	require.Equal(t, AttemptUpdatedObservedAtID, Migrations[len(Migrations)-1].ID)
	for _, m := range Migrations[:len(Migrations)-1] {
		require.Greater(t, AttemptUpdatedObservedAtID, m.ID)
	}
	db := openDB(t)
	require.NoError(t, NewAssignmentsSchema().Migrate(db))
	// Previous-release pods insert attempts without the column, before and during the overlap.
	oldInsert := `INSERT INTO update_attempts
	 (tenant_id, token, assignment_id, device_token, component, target_version, target_digest, requires_reboot,
	  state, version, last_seq, last_report_at, boot_id_at_start, install_boot_id, last_boot_id, last_stage,
	  cancel_requested, reconciled, failure_stage, failure_code, failure_detail, failure_platform)
	 VALUES ('acme', ?, ?, 'dev-1', 'firmware', '1.0.0', '', false, 'UPDATED', 1, 1, CURRENT_TIMESTAMP, '', '', '', '', false, false, '', '', '', false)`
	require.NoError(t, db.Exec(oldInsert, "before", 1).Error)
	for range 3 {
		require.NoError(t, NewAttemptUpdatedObservedAtSchema().Migrate(db))
	}
	require.NoError(t, db.Exec(oldInsert, "during", 2).Error)
	cols, err := db.Migrator().ColumnTypes("update_attempts")
	require.NoError(t, err)
	found := false
	for _, c := range cols {
		if c.Name() == "updated_observed_at" {
			found = true
			nullable, ok := c.Nullable()
			require.True(t, ok)
			require.True(t, nullable)
		}
	}
	require.True(t, found)
	var count int64
	require.NoError(t, db.Table("update_attempts").Where("updated_observed_at IS NULL").Count(&count).Error)
	require.EqualValues(t, 2, count, "nothing is backfilled")
}
