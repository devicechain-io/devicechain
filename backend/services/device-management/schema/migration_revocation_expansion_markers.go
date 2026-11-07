// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// pendingRevocationExpansion is the SNAPSHOT of the revocation-kick marker as of this migration. It is
// not model.PendingRevocationExpansion and must never be replaced by it, for the reason every shape in
// this package gives (see migration_device_replacements.go).
//
// # What the table is
//
// One row per revocation write that still owes its per-device kick rows. A revocation that ends
// live sessions used to write one pending_revocation_kicks row per affected device while holding its
// trust anchor row FOR UPDATE, which for a CA-level entry over a fleet is seconds of lock held
// against every certificate registration. It now writes this single marker inside the lock and
// expands it into the per-device rows in a second transaction that holds no anchor lock; the
// background drain expands any marker nobody expanded. The row is deleted in the transaction that
// writes the device rows, so it is empty whenever every revocation has been expanded.
//
// Kind is MANUAL (Fingerprint names the entry) or CRL (IssuerKey and IssuerName name the issuer,
// and the serials the upload newly revoked are the pendingRevocationExpansionSerial rows of the same
// Generation). Ca and LinksWhole are the revocation writer's targeting inputs.
type pendingRevocationExpansion struct {
	TenantId      string    `gorm:"primaryKey;not null;size:128"`
	TrustAnchorId uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation    int64     `gorm:"primaryKey;not null;autoIncrement:false"`
	Kind          string    `gorm:"not null;size:16"`
	Ca            bool      `gorm:"not null"`
	LinksWhole    bool      `gorm:"not null"`
	Fingerprint   *string   `gorm:"size:64"`
	IssuerKey     *string   `gorm:"size:64"`
	IssuerName    *string   `gorm:"size:64"`
	RequestedAt   time.Time `gorm:"not null"`
}

// pendingRevocationExpansionSerial is the SNAPSHOT of the serials a CRL upload newly revoked, kept until
// its marker is expanded. They are written before the anchor row is locked, so the lock covers
// only the marker row.
type pendingRevocationExpansionSerial struct {
	TenantId      string `gorm:"primaryKey;not null;size:128"`
	TrustAnchorId uint   `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation    int64  `gorm:"primaryKey;not null;autoIncrement:false"`
	Serial        string `gorm:"primaryKey;not null;size:64"`
}

// TableName is NOT declared on either type: it would bypass the area's TablePrefix (see
// migration_list_order_indexes.go); the type names derive pending_revocation_expansions and
// pending_revocation_expansion_serials.

// NewRevocationExpansionMarkersSchema creates the two tables.
//
// # Additive only
//
// Two new tables, no column on an existing one, no foreign key (the kick queue has none either: a
// marker outlives a deleted anchor and the drain clears it). A replica still running the previous
// release writes its per-device rows directly and never reads these tables; a replica running this
// one drains both.
//
// # No backfill
//
// A row stands for a revocation not yet expanded; nothing earlier is owed.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. AutoMigrate
// creates each table only when it is absent.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven tenant purge classifies and erases them with
// no exemption to register.
func NewRevocationExpansionMarkersSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261112100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&pendingRevocationExpansion{}, &pendingRevocationExpansionSerial{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"pending_revocation_expansion_serials", "pending_revocation_expansions"})
		},
	}
}
