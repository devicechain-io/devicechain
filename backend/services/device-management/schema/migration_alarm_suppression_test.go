// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestAlarmSuppressionMigrationReplays(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := NewAlarmSuppressionSchema()
	for range 2 {
		require.NoError(t, migration.Migrate(db))
	}
	require.True(t, db.Migrator().HasColumn(&alarmSuppressionWindow{}, "creator_scope"))
	w := alarmSuppressionWindow{TenantId: "acme", Token: "service", Reason: "work", Targets: []byte(`[{"type":"TENANT","token":""}]`), Kind: "ONE_SHOT"}
	require.NoError(t, db.Create(&w).Error)
	duplicate := w
	duplicate.ID = 0
	require.Error(t, db.Create(&duplicate).Error)
	require.NoError(t, db.Delete(&w).Error)
	duplicate.ID = 0
	require.NoError(t, db.Create(&duplicate).Error)
	stamp := alarmCycleSuppression{TenantId: "acme", AlarmId: 7, RaisedTime: time.Now().UTC(), AlarmToken: "alarm", WindowToken: "service"}
	require.NoError(t, db.Create(&stamp).Error)
	stamp.ID = 0
	require.Error(t, db.Create(&stamp).Error)
	stamp.ID = 0
	stamp.RaisedTime = stamp.RaisedTime.Add(time.Second)
	require.NoError(t, db.Create(&stamp).Error)
	var count int64
	require.NoError(t, db.Model(&alarmCycleSuppression{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
