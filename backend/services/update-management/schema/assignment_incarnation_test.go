// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssignmentIncarnationMigrationIsNullableRerunnableAndOrdered(t *testing.T) {
	require.Equal(t, AssignmentIncarnationID, Migrations[9].ID)
	for _, m := range Migrations[:9] {
		require.Greater(t, AssignmentIncarnationID, m.ID)
	}
	db := openDB(t)
	require.NoError(t, NewAssignmentsSchema().Migrate(db))
	for range 3 {
		require.NoError(t, NewAssignmentIncarnationSchema().Migrate(db))
	}
	cols, err := db.Migrator().ColumnTypes("update_assignments")
	require.NoError(t, err)
	found := false
	for _, c := range cols {
		if c.Name() == "device_incarnation" {
			found = true
			nullable, ok := c.Nullable()
			require.True(t, ok)
			require.True(t, nullable)
		}
	}
	require.True(t, found)
	// An older pod's insert omits the new column and must still work.
	require.NoError(t, db.Exec(`INSERT INTO update_assignments
  (tenant_id, token, device_token, component, artifact_id) VALUES ('acme', 'asg-1', 'dev-1', 'firmware', 1)`).Error)
	var count int64
	require.NoError(t, db.Table("update_assignments").Where("device_incarnation IS NULL").Count(&count).Error)
	require.Equal(t, int64(1), count)
}
