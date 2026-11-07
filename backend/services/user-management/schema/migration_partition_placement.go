// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// The placement table (core/partition): which shard group owns each of the instance's P
// vpartitions, and which tenants are pinned to a vpartition by hand. Three small tables, all
// instance-global, all new — so nothing here touches a row an older pod writes, and a rolling
// upgrade needs no expand/contract.
//
// 🔴 NOTHING READS THESE YET. They are the authoritative copy that a KV mirror is built from, and
// the mirror is consumed by no routing path in this release.
//
// Each type below is this migration's own snapshot, never the live iam model (house rule in
// migrations.go). The check constraints are part of the snapshot on purpose: they are the
// database's own refusal of a second config row and of a vpartition or group below zero, which
// holds even when the application's guards are bypassed. P's immutability is NOT expressible as
// a check constraint (it compares a new row to an old one); it lives in iam.Store.EnsurePlacement
// and, again, in partition.Publish.

// partitionConfigSnapshot is the single row recording P. The primary key is pinned to 1.
type partitionConfigSnapshot struct {
	ID         uint `gorm:"primarykey;autoIncrement:false;check:chk_iam_partition_config_singleton,id = 1"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	P          int   `gorm:"not null;check:chk_iam_partition_config_p,p >= 1 AND p <= 1024"`
	Generation int64 `gorm:"not null;default:0;check:chk_iam_partition_config_generation,generation >= 0"`
}

func (partitionConfigSnapshot) TableName() string { return "iam_partition_config" }

// vpartitionAssignmentSnapshot is one vpartition's shard group.
type vpartitionAssignmentSnapshot struct {
	VPartition int `gorm:"column:vpartition;primarykey;autoIncrement:false;check:chk_iam_vpartition_assignments_vp,vpartition >= 0"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ShardGroup int `gorm:"not null;default:0;check:chk_iam_vpartition_assignments_group,shard_group >= 0"`
}

func (vpartitionAssignmentSnapshot) TableName() string { return "iam_vpartition_assignments" }

// tenantVPartitionOverrideSnapshot pins one tenant token to a vpartition.
//
// The column is tenant_token, NOT tenant or tenant_id: those two names are what the tenant-scope
// callback and the tenant purge treat as "this row belongs to a tenant and is swept with it", and
// a pin must outlive the purge — the sweep finds the tenant's stores through its placement.
type tenantVPartitionOverrideSnapshot struct {
	TenantToken string `gorm:"primarykey;size:128"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	VPartition  int `gorm:"column:vpartition;not null;check:chk_iam_tenant_vpartition_overrides_vp,vpartition >= 0"`
}

func (tenantVPartitionOverrideSnapshot) TableName() string { return "iam_tenant_vpartition_overrides" }

// NewPartitionPlacementMigration creates the placement tables. It seeds NOTHING: the config row
// and the 64 assignments are written by iam.Store.EnsurePlacement at start, so a fresh install
// and an upgraded one reach the same state by the same code, and no row's value lives in a frozen
// migration.
//
// Re-runnable by construction: AutoMigrate creates what is missing and drops nothing, which
// matters because migrations run with UseTransaction:false and replay from the top after a
// failure.
func NewPartitionPlacementMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261105120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&partitionConfigSnapshot{},
				&vpartitionAssignmentSnapshot{},
				&tenantVPartitionOverrideSnapshot{},
			)
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(
				&tenantVPartitionOverrideSnapshot{},
				&vpartitionAssignmentSnapshot{},
				&partitionConfigSnapshot{},
			)
		},
	}
}
