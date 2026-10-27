// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// pendingSessionKick is the SNAPSHOT of the session-kick queue as of this migration. It is
// not model.PendingSessionKick and must never be replaced by it, for the reason every shape
// in this package gives (see migration_device_replacements.go).
//
// # What the table is
//
// One row per device whose authority a committed provisioning revoke or reset withdrew and
// whose live broker sessions have not yet been confirmed ended. The write inserts the row in
// its own transaction; the kick deletes it once the broker's responder confirmed. Without it
// a repeat of the operation, which finds no credential left to change, could not know the
// kick was still owed. It is a queue, so it is empty whenever every kick has been confirmed.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it
// with no exemption to register.
type pendingSessionKick struct {
	TenantId              string `gorm:"primaryKey;not null;size:128"`
	DeviceToken           string `gorm:"primaryKey;not null;size:255"`
	Reason                string `gorm:"not null;size:64"`
	ProvisioningProfileId *uint
	Generation            int64     `gorm:"not null"`
	RequestedAt           time.Time `gorm:"not null"`
}

// TableName is NOT declared: it would bypass the area's TablePrefix (see
// migration_list_order_indexes.go); the type name derives pending_session_kicks.

// NewPendingSessionKicksSchema creates the session-kick queue.
//
// # No backfill
//
// A row stands for a kick not yet confirmed; nothing recorded which earlier ones were, and
// there is nothing to write for them.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure.
// AutoMigrate creates the table only when it is absent.
func NewPendingSessionKicksSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261027120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&pendingSessionKick{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"pending_session_kicks"})
		},
	}
}
