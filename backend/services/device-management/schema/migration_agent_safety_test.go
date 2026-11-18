// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAgentSafetyMigrationReplaysAndKeepsTenantColumns(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	migration := NewAgentSafetySchema()
	for range 2 {
		require.NoError(t, migration.Migrate(db))
	}
	for _, table := range []string{"agent_actuation_fences", "agent_effect_outboxes"} {
		require.True(t, db.Migrator().HasTable(table), table)
		require.True(t, db.Migrator().HasColumn(table, "tenant_id"), table)
	}
	require.True(t, db.Migrator().HasIndex(&agentEffectOutbox{}, "CreatedAt"))
}
