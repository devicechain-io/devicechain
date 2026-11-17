// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// pendingEntityDeletion is this migration's own snapshot, never a live model.
//
// # What the table is
//
// Each immutable row records a committed entity deletion awaiting broker acknowledgment.
// It commits with the delete so a crash or outage cannot lose downstream cleanup.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge erases it.
type pendingEntityDeletion struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;index:idx_pending_entity_deletions_tenant_id,priority:2"`
	TenantId    string    `gorm:"not null;size:128;index:idx_pending_entity_deletions_tenant_id,priority:1"`
	EntityType  string    `gorm:"not null;size:64"`
	EntityId    uint      `gorm:"not null"`
	EntityToken string    `gorm:"not null;size:255"`
	DeletedTime time.Time `gorm:"not null"`
}

// TableName is NOT declared: it would bypass the area's TablePrefix.

// NewPendingEntityDeletionsSchema creates the entity deletion outbox.
//
// # No backfill
//
// Historical deletions were not recorded, so there are no facts to reconstruct.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay after a failure. AutoMigrate
// creates the table only when absent, so replay is safe.
func NewPendingEntityDeletionsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID:       "20261215120000",
		Migrate:  func(tx *gorm.DB) error { return tx.AutoMigrate(&pendingEntityDeletion{}) },
		Rollback: func(tx *gorm.DB) error { return dropTables(tx, []string{"pending_entity_deletions"}) },
	}
}
