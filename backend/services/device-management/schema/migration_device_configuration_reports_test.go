// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestConfigurationReportsMigrationReruns(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	registered := false
	for _, migration := range Migrations {
		registered = registered || migration.ID == NewDeviceConfigurationReportsSchema().ID
	}
	require.True(t, registered, "report migration is not registered")
	require.NoError(t, NewDeviceConfigurationSchema().Migrate(db))
	require.NoError(t, db.Exec(`INSERT INTO device_configuration_states (tenant_id,device_id,reported_revision,reported_status) VALUES ('t',1,5,'REJECTED')`).Error)
	for range 2 {
		require.NoError(t, NewDeviceConfigurationReportsSchema().Migrate(db))
	}
	for _, column := range []string{"captured_revision", "captured_status", "capture_gap_through", "capture_complete_since", "captured_reported_at"} {
		require.True(t, db.Migrator().HasColumn("device_configuration_states", column), column)
		cols, err := db.Migrator().ColumnTypes("device_configuration_states")
		require.NoError(t, err)
		for _, col := range cols {
			if col.Name() == column {
				nullable, known := col.Nullable()
				require.True(t, known && nullable, column)
				_, hasDefault := col.DefaultValue()
				require.False(t, hasDefault, column)
			}
		}
	}
	require.True(t, db.Migrator().HasTable("device_configuration_reports"))
	require.True(t, db.Migrator().HasColumn("device_configuration_reports", "tenant_id"))
	var untouched int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM device_configuration_states WHERE reported_revision=5 AND reported_status='REJECTED' AND captured_revision IS NULL AND captured_status IS NULL AND capture_gap_through IS NULL AND capture_complete_since IS NULL AND captured_reported_at IS NULL`).Scan(&untouched).Error)
	require.Equal(t, int64(1), untouched)
	require.Error(t, db.Exec(`INSERT INTO device_configuration_reports (created_at,tenant_id,device_id,revision,digest,status,source) VALUES (CURRENT_TIMESTAMP,'t',1,1,'d','RECEIVED','REPORT')`).Error)
}
