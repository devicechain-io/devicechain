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
// The lifecycle columns added by the previous migration say a tenant's purge has
// STARTED. Nothing said it finished, and nothing could: the tenant row is removed at
// completion (that is what releases the token), so the row cannot be where completion
// is recorded. These two tables are where it goes instead.
//
// The record is keyed by (token, epoch) rather than by token. Releasing the token means
// the same token can be purged more than once over an instance's life, so a record keyed
// by token alone would be overwritten by a successor's purge — erasing the evidence that
// the predecessor was erased.
//
// Each migration declares its own snapshot types. These two are new tables rather than
// altered ones, so their snapshots are the whole shape; when the live models gain a
// field, this migration keeps creating the shape that was current when it was written and
// a later migration adds the column.

// tenantPurgeSnapshot is the deletion record: one row per (token, epoch) purge.
//
// 🔴 IT DELIBERATELY DOES NOT CARRY THE TENANT'S NAME. A deletion record has to name
// what was deleted, and the token is unavoidable for that — it is the identity every
// other area stored. The display name is avoidable, so it is not kept: retaining
// "Acme Manufacturing GmbH" in a table that survives the erasure would make the record
// of the erasure into the last place the customer's details still live.
type tenantPurgeSnapshot struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// Token and Epoch are the record's identity, unique together. Epoch is copied from
	// the tenant row at the moment the purge began, so the record and every fence that
	// outlives the tenant agree on which purge they are talking about.
	Token string    `gorm:"not null;size:128;uniqueIndex:idx_tenant_purges_token_epoch,priority:1"`
	Epoch time.Time `gorm:"not null;uniqueIndex:idx_tenant_purges_token_epoch,priority:2"`

	// CompletedAt is nil while stores are still reporting. It is set in the same
	// transaction that removes the tenant row, so "the token was released" and "the
	// erasure was recorded complete" cannot come apart.
	CompletedAt *time.Time `gorm:"index"`
	// Rows is the total erased across every store, for the record only.
	Rows int64 `gorm:"not null;default:0"`
}

func (tenantPurgeSnapshot) TableName() string { return "iam_tenant_purges" }

// tenantPurgeStoreSnapshot is one store's line in a purge's ledger: what the relational
// database, the event store, the broker and so on each reported on the latest pass.
//
// One row per (purge, store), rewritten each pass rather than appended, because what the
// coordinator needs is the CURRENT state of each store, not a history of attempts. Every
// pass re-runs every store — the erasures are idempotent and a repeat costs a delete that
// matches nothing — so a row is a standing claim that is re-established, not a log entry
// that could go stale without anyone noticing.
type tenantPurgeStoreSnapshot struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantPurgeID uint   `gorm:"not null;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:1"`
	Store         string `gorm:"not null;size:32;uniqueIndex:idx_tenant_purge_stores_purge_store,priority:2"`
	// The association exists to make AutoMigrate emit the foreign key; nothing reads
	// it through this type. CASCADE rather than RESTRICT because a ledger line has no
	// meaning without the record it explains.
	TenantPurge *tenantPurgeSnapshot `gorm:"foreignKey:TenantPurgeID;constraint:OnDelete:CASCADE"`

	// Complete is the only field completion consults. Rows and Deferred explain it.
	Complete bool  `gorm:"not null;default:false"`
	Rows     int64 `gorm:"not null;default:0"`
	// Deferred names what this store still holds and did not erase. It is the reason
	// Complete is false, in words, and it is written for a reader deciding whether an
	// erasure claim can be made — so it must name the data, not the ticket.
	Deferred string
	// Failure is the last error, empty on success. Distinct from Deferred: a failure is
	// expected to clear on the next pass, a deferral is not.
	Failure     string
	AttemptedAt time.Time
	// CleanSince is when this store FIRST reported complete and has reported complete on
	// every pass since. Cleared the moment it reports anything else.
	//
	// It exists because "the delete returned no error" and "nothing came back" are
	// different claims, and only the second one can support an erasure record. A
	// residual scan run immediately after the sweep sees only writes already in flight;
	// a straggler admitted before the ingest fence noticed the deletion lands after it.
	// Requiring a store to have been clean for a settle window turns one observation
	// into a sustained one.
	CleanSince *time.Time
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
