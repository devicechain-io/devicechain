// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// pendingOwnershipFact is the SNAPSHOT of the ownership outbox as of this migration. It is
// not model.PendingOwnershipFact and must never be replaced by it, for the reason every
// shape in this package gives (see migration_device_replacements.go).
//
// # What the table is
//
// One row per device whose owners a committed write may have changed and device-state has
// not yet been told of (ADR-081). The write upserts the row in its own transaction; the
// ownership drain publishes the device's current anchors and deletes the row once the broker
// has acknowledged them. The primary key is what folds a device changed twice before the
// drain reached it into one row, and Generation is what keeps a delete from swallowing a
// change committed while its fact was being published. It is a queue, so it is empty
// whenever the drain has caught up.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it
// with no exemption to register: a purged tenant's undelivered facts go with it.
type pendingOwnershipFact struct {
	TenantId    string    `gorm:"primaryKey;not null;size:128"`
	DeviceId    uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation  int64     `gorm:"not null"`
	RequestedAt time.Time `gorm:"not null"`
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// migration_list_order_indexes.go), and the type name already derives the table name
// pending_ownership_facts.

// NewPendingOwnershipFactsSchema creates the ownership outbox.
//
// # No backfill
//
// A row stands for a change not yet announced. Every change committed before this ran was
// announced, or not, by the post-commit publish it replaces, and nothing records which; there
// is no row to write for it. A device whose fact was lost before the upgrade is repaired, as
// it was before, by its next event.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure.
// AutoMigrate creates the table only when it is absent.
func NewPendingOwnershipFactsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016200000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&pendingOwnershipFact{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"pending_ownership_facts"})
		},
	}
}
