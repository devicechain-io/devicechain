// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantPurgeErasedAtSnapshot is this migration's own snapshot of iam_tenant_purges: the
// primary key, and the one column it adds. Nothing else. It is a snapshot, not the live
// model, per the house rule in migrations.go.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type name
// and the migration silently creates a stray table instead of touching the deletion record.
// TestTheErasedAtMigrationAddsTheColumnToTheDeletionRecord asserts the stray table's absence
// as well as the column's presence.
type tenantPurgeErasedAtSnapshot struct {
	ID uint `gorm:"primarykey"`

	// Nullable and untagged: null means no erasure claim is standing.
	ErasedAt *time.Time
}

func (tenantPurgeErasedAtSnapshot) TableName() string { return "iam_tenant_purges" }

// NewTenantPurgeErasedAtMigration adds the deletion record's erased_at column.
//
// completed_at is stamped when the token is released, which is at least the token hold
// (12 hours by default) after the data was gone. A customer or auditor asking "is my data
// erased" therefore had no durable answer for the first half day, only a live reading of the
// ledger. erased_at is the standing claim "every storage system has reported clean, and has
// stayed clean for the settle window"; the coordinator clears it if a store regresses.
//
// AutoMigrate is individually re-runnable: it adds the column if absent and is a no-op if
// present, which the chain requires (migrations run with UseTransaction:false).
func NewTenantPurgeErasedAtMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261029120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantPurgeErasedAtSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&tenantPurgeErasedAtSnapshot{}, "erased_at")
		},
	}
}
