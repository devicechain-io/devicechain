// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationReviewEnforcementMigrationIsReplayable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE device_profile_versions (id integer PRIMARY KEY)").Error)
	migration := NewAutomationReviewEnforcementSchema()
	require.Equal(t, "20261126120000", migration.ID)
	require.Equal(t, migration.ID, Migrations[len(Migrations)-1].ID)
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, migration.Migrate(db))
	for _, table := range []string{"automation_review_policies", "automation_review_policy_changes", "automation_review_break_glass"} {
		require.True(t, db.Migrator().HasTable(table), table)
		require.False(t, db.Migrator().HasColumn(table, "deleted_at"), table)
		require.True(t, db.Migrator().HasColumn(table, "tenant_id"), table)
	}
	require.True(t, db.Migrator().HasColumn("device_profile_versions", "automation_review_token"))
	require.Error(t, migration.Rollback(db))
	require.True(t, db.Migrator().HasTable("automation_review_policies"))
}
