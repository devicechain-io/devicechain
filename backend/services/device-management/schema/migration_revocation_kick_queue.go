// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// pendingRevocationKick is the SNAPSHOT of the certificate-revocation kick queue as of this
// migration. It is not model.PendingRevocationKick and must never be replaced by it, for the
// reason every shape in this package gives (see migration_device_replacements.go).
//
// It has the shape of pending_session_kicks plus not_before (the instant a re-sweep may be
// drained). Origin is the trust anchor's id.
type pendingRevocationKick struct {
	TenantId    string    `gorm:"primaryKey;not null;size:128"`
	DeviceToken string    `gorm:"primaryKey;not null;size:255"`
	Origin      uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation  int64     `gorm:"not null"`
	RequestedAt time.Time `gorm:"not null"`
	NotBefore   *time.Time
}

// TableName is NOT declared: it would bypass the area's TablePrefix; the type name derives
// pending_revocation_kicks.

// NewRevocationKickQueueSchema creates the certificate-revocation kick queue and records, on a
// manual revocation entry, that its kick debt was queued.
//
//  1. pending_revocation_kicks is created. It is a table of its own and NOT more rows of
//     pending_session_kicks because a release that predates it drains EVERY row of that table for a
//     tenant, deletes them after one sweep and reads an origin it does not know as a provisioning
//     profile id: during a rolling upgrade it would delete the new rows and lose the delayed
//     re-sweep. Nothing else could hide a row from a query that selects the whole table. The
//     not_before column this chain added to pending_session_kicks (migration_x509_revocation.go) is
//     left in place, nullable and no longer written.
//  2. device_revoked_certificates gains kick_queued_at (nullable, no default): set when the entry's
//     kick debt was queued, so repeating a revocation that predates the debt queues it once.
//
// Nullable, no default, no backfill; DDL only. Every statement is re-runnable (migrations run with
// UseTransaction:false and replay from the top): AutoMigrate creates the table only when it is
// absent, the column is ADD COLUMN IF NOT EXISTS.
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it with no
// exemption to register. There is no rollback: dropping the queue would forget kicks that were owed.
func NewRevocationKickQueueSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261111120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&pendingRevocationKick{}); err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE "device-management"."device_revoked_certificates" ` +
				`ADD COLUMN IF NOT EXISTS kick_queued_at timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the certificate-revocation kick queue migration cannot be rolled back: " +
				"dropping the queue would forget kicks that are still owed")
		},
	}
}
