// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// ADR-077 slice 2b: the deletion record, and the per-store ledger underneath it.
//
// The lifecycle columns added by the previous migration say a tenant's purge has STARTED.
// Nothing said it finished, and nothing could: the tenant row is removed at completion
// (that is what releases the token), so the row cannot be where completion is recorded.
// These two tables are where it goes instead.
//
// 🔴 THIS FILE DESCRIBES A SHAPE, NOT A DESIGN, AND THE DIFFERENCE IS ENFORCED BY THE
// HOUSE RULE. A migration is never edited once it lands, so any rationale written here is
// frozen while the thing it explains keeps moving — a comment guaranteed to drift into
// being wrong, in the file a reader opens precisely to find out what the table looked
// like. Why the record is keyed (token, epoch), why it carries no display name, and what
// clean_since is for all live on iam.TenantPurge and iam.TenantPurgeStore, which are free
// to change. What stays here is what a reader of this DDL cannot get anywhere else.
//
// Each migration declares its own snapshot types. These two are new tables rather than
// altered ones, so their snapshots are the whole shape; when the live models gain a field,
// this migration keeps creating the shape that was current when it was written and a later
// migration adds the column.

// tenantPurgeSnapshot is the deletion record: one row per (token, epoch) purge.
type tenantPurgeSnapshot struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// Unique TOGETHER — a token may be purged more than once, since completion releases
	// it. The priorities are what make it one composite index rather than two.
	Token string    `gorm:"not null;size:128;uniqueIndex:idx_tenant_purges_token_epoch,priority:1"`
	Epoch time.Time `gorm:"not null;uniqueIndex:idx_tenant_purges_token_epoch,priority:2"`

	CompletedAt *time.Time `gorm:"index"`
	Rows        int64      `gorm:"not null;default:0"`
}

func (tenantPurgeSnapshot) TableName() string { return "iam_tenant_purges" }

// tenantPurgeStoreSnapshot is one store's line in a purge's ledger.
type tenantPurgeStoreSnapshot struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// Unique together: one line per store per purge, rewritten rather than appended.
	TenantPurgeID uint   `gorm:"not null;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:1"`
	Store         string `gorm:"not null;size:32;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:2"`

	Complete    bool  `gorm:"not null;default:false"`
	Rows        int64 `gorm:"not null;default:0"`
	Deferred    string
	Failure     string
	AttemptedAt time.Time
	CleanSince  *time.Time

	// The association exists to make AutoMigrate emit the foreign key; nothing reads it
	// through this type. CASCADE rather than RESTRICT because a ledger line has no
	// meaning without the record it explains.
	TenantPurge *tenantPurgeSnapshot `gorm:"foreignKey:TenantPurgeID;constraint:OnDelete:CASCADE"`
}

func (tenantPurgeStoreSnapshot) TableName() string { return "iam_tenant_purge_stores" }

// NewTenantPurgeRecordMigration creates the deletion record and its per-store ledger.
//
// Re-runnable by construction: AutoMigrate creates what is missing and drops nothing,
// which matters because migrations run with UseTransaction:false and replay from the top
// after a failure.
func NewTenantPurgeRecordMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260803210000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantPurgeSnapshot{}, &tenantPurgeStoreSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&tenantPurgeStoreSnapshot{}, &tenantPurgeSnapshot{})
		},
	}
}
