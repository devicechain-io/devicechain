// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestPendingAlarmEventsMigrationReplays(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := NewPendingAlarmEventsSchema()
	for range 2 {
		require.NoError(t, migration.Migrate(db))
	}
	require.True(t, db.Migrator().HasConstraint(&pendingAlarmEvent{}, "chk_pending_alarm_events_tenant_id"))
	var indexSQL string
	require.NoError(t, db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_pending_alarm_events_tenant_id'").Scan(&indexSQL).Error)
	require.Contains(t, indexSQL, "WHERE quarantined_at IS NULL")
	require.Error(t, db.Create(&pendingAlarmEvent{Payload: []byte(`{}`), CreatedAt: time.Now()}).Error)
	require.True(t, db.Migrator().HasIndex(&pendingAlarmEvent{}, "idx_pending_alarm_events_tenant_id"))
	require.True(t, db.Migrator().HasColumn(&pendingAlarmEvent{}, "quarantined_at"))
	require.True(t, db.Migrator().HasColumn(&pendingAlarmEvent{}, "last_error"))
	row := pendingAlarmEvent{TenantId: "acme", Payload: []byte(`{"AlarmToken":"alarm"}`), CreatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, migration.Migrate(db))
	var got pendingAlarmEvent
	require.NoError(t, db.First(&got, row.ID).Error)
	require.Equal(t, row.Payload, got.Payload)
	require.Nil(t, got.QuarantinedAt)
}

// Replay also repairs a missing CHECK on an already-created table.
func TestPendingAlarmEventsReplayCreatesMissingCheck(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := NewPendingAlarmEventsSchema()
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, db.Migrator().DropConstraint(&pendingAlarmEvent{}, "chk_pending_alarm_events_tenant_id"))
	require.False(t, db.Migrator().HasConstraint(&pendingAlarmEvent{}, "chk_pending_alarm_events_tenant_id"))
	for range 2 {
		require.NoError(t, migration.Migrate(db))
	}
	require.True(t, db.Migrator().HasConstraint(&pendingAlarmEvent{}, "chk_pending_alarm_events_tenant_id"))
	require.Error(t, db.Create(&pendingAlarmEvent{Payload: []byte(`{}`), CreatedAt: time.Now()}).Error)
}
