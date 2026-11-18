// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeletionAlarmCascadeMigrationReplaysAndAllowsOldWrites(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, NewPendingEntityDeletionsSchema().Migrate(db))
	migration := NewDeletionAlarmCascadeSchema()
	for range 2 {
		require.NoError(t, migration.Migrate(db))
	}
	// The released snapshot still inserts successfully without naming the new column.
	old := &pendingEntityDeletion{TenantId: "acme", EntityType: "device", EntityToken: "gone", DeletedTime: time.Now().UTC()}
	require.NoError(t, db.Create(old).Error)
	type pendingEntityDeletion struct{ Cascade *string }
	var row pendingEntityDeletion
	require.NoError(t, db.First(&row).Error)
	require.Nil(t, row.Cascade)
	columns, err := db.Migrator().ColumnTypes(&pendingEntityDeletion{})
	require.NoError(t, err)
	found := false
	for _, column := range columns {
		if column.Name() == "cascade" {
			found = true
			require.Equal(t, "text", column.DatabaseTypeName())
		}
	}
	require.True(t, found)
}
