// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantPurgeStoreNoteSnapshot is this migration's own snapshot of iam_tenant_purge_stores:
// the primary key, and the one column it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go. Pointing this
// at iam.TenantPurgeStore would make this migration mean whatever that struct means on the
// day it runs — which breaks FRESH installs the moment the model gains another field (the
// baseline creates the table without it, this migration adds two columns, and the next
// fresh install replays into "column already exists") while every existing database applies
// cleanly and looks healthy.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type name
// and this migration silently creates a `tenant_purge_store_note_snapshots` table instead of
// touching the ledger — succeeding, migrating nothing, and leaving the column that every
// writer above it now assigns to. TestTheNoteMigrationAddsTheColumnToTheLedgerTable is the
// guard, and it asserts the absence of the stray table as well as the presence of the column.
type tenantPurgeStoreNoteSnapshot struct {
	ID uint `gorm:"primarykey"`

	// Untagged, like Deferred and Failure beside it: plain nullable text. Deliberately not
	// length-capped — the key-value store's note is the platform's bucket exemptions,
	// which are paragraphs of reasoning rather than labels, and a cap sized for today's
	// wording would truncate the next one silently.
	Note string
}

func (tenantPurgeStoreNoteSnapshot) TableName() string { return "iam_tenant_purge_stores" }

// NewTenantPurgeNoteMigration adds the ledger's note column (ADR-077).
//
// Two stores could report clean without recording what they had skipped — the telemetry
// store when there is no telemetry database to sweep, and the key-value store, which never
// mentioned its exempted buckets. Both logged a warning and then wrote a ledger line
// byte-identical to a store that swept the cluster and found nothing. This column is where
// that judgement goes, so the durable evidence of an erasure carries the reasoning and not
// only the result.
//
// It is a note rather than a deferral because neither case may block completion: an
// instance running no event-management has no telemetry to erase, and the exempted buckets
// hold nothing belonging to the tenant.
//
// AutoMigrate is individually re-runnable — it adds the column if it is absent and is a
// no-op if it is present — which the chain requires, since migrations run with
// UseTransaction:false and replay from the top after a failure.
func NewTenantPurgeNoteMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260804140000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantPurgeStoreNoteSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&tenantPurgeStoreNoteSnapshot{}, "note")
		},
	}
}
