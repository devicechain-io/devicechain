// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Independent read-back types, deliberately not the migration's snapshots, so a wrong TableName
// cannot pass by being wrong in both places.
type placementConfigRow struct {
	ID         uint
	P          int
	Generation int64
}

func (placementConfigRow) TableName() string { return "iam_partition_config" }

type placementAssignmentRow struct {
	VPartition int `gorm:"column:vpartition;primarykey"`
	ShardGroup int
}

func (placementAssignmentRow) TableName() string { return "iam_vpartition_assignments" }

type placementOverrideRow struct {
	TenantToken string `gorm:"primarykey"`
	VPartition  int    `gorm:"column:vpartition"`
}

func (placementOverrideRow) TableName() string { return "iam_tenant_vpartition_overrides" }

func TestThePlacementMigrationCreatesTheThreeTables(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Create(&placementConfigRow{ID: 1, P: 64}).Error)
	require.NoError(t, db.Create(&placementAssignmentRow{VPartition: 0, ShardGroup: 0}).Error)
	require.NoError(t, db.Create(&placementOverrideRow{TenantToken: "acme", VPartition: 3}).Error)

	var cfg placementConfigRow
	require.NoError(t, db.First(&cfg).Error)
	require.Equal(t, 64, cfg.P)
	require.Equal(t, int64(0), cfg.Generation, "generation defaults to 0")
}

// It seeds nothing: the rows belong to EnsurePlacement, so no value lives in a frozen migration.
func TestThePlacementMigrationSeedsNothing(t *testing.T) {
	db := newMigratedDB(t)
	for _, table := range []string{"iam_partition_config", "iam_vpartition_assignments", "iam_tenant_vpartition_overrides"} {
		var n int64
		require.NoError(t, db.Table(table).Count(&n).Error)
		require.Equal(t, int64(0), n, table)
	}
}

func TestThePlacementMigrationDoesNotCreateStrayTables(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{
		"partition_config_snapshots", "vpartition_assignment_snapshots",
		"tenant_v_partition_override_snapshots", "tenant_vpartition_override_snapshots",
	} {
		require.False(t, db.Migrator().HasTable(stray), stray)
	}
}

// The database itself refuses a second config row, an out-of-range P and a negative index, so
// the guarantees hold even for a writer that bypasses iam.Store.
func TestThePlacementMigrationConstraintsRefuseBadRows(t *testing.T) {
	db := newMigratedDB(t)
	require.Error(t, db.Create(&placementConfigRow{ID: 2, P: 64}).Error, "a second config row")
	require.Error(t, db.Create(&placementConfigRow{ID: 1, P: 0}).Error, "P below 1")
	require.Error(t, db.Create(&placementConfigRow{ID: 1, P: 1025}).Error, "P above the ceiling")
	require.Error(t, db.Create(&placementAssignmentRow{VPartition: -1}).Error)
	require.Error(t, db.Create(&placementAssignmentRow{VPartition: 1, ShardGroup: -1}).Error)
	require.Error(t, db.Create(&placementOverrideRow{TenantToken: "x", VPartition: -1}).Error)
}

// Re-runnable: migrations replay from the top after a failure.
func TestThePlacementMigrationIsRerunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Create(&placementConfigRow{ID: 1, P: 64}).Error)
	require.NoError(t, NewPartitionPlacementMigration().Migrate(db))
	var n int64
	require.NoError(t, db.Model(&placementConfigRow{}).Count(&n).Error)
	require.Equal(t, int64(1), n, "a replay must not disturb existing rows")
}
