// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// ADR-077: a tenant gains a LIFECYCLE. Deleting one used to hard-delete a single row,
// which freed its token for reuse — and since the tenant token IS the isolation key
// every other area stores (rdb.TenantScoped.TenantId), a tenant recreated at a used
// token inherited its predecessor's devices, dashboards, telemetry and secrets.
//
// These two columns are what close that: the row now survives the delete, so the
// token stays taken, and the epoch dates the cut so a later sweep can tell the purged
// tenant's residue from a successor's legitimate rows.
//
// 🔴 THIS IS THIS AREA'S FIRST MIGRATION APPENDED AFTER THE GA SQUASH, so it is also
// the worked example of the rule: it declares its OWN snapshot of the columns it
// touches and never references the live iam.Tenant. A migration that pointed at the
// live model would be silently rewritten by the next field added to that model —
// breaking FRESH installs with "column already exists" while every existing database
// applied cleanly and looked healthy.

// tenantPurgeStateSnapshot is this migration's snapshot of iam_tenants: the primary
// key gorm needs to target the table, plus exactly the two columns being added.
//
// TableName is pinned to match the baseline's own tenant snapshot; leaving it off would
// derive a table from the Go type name and quietly migrate nothing. (Nearly every
// snapshot in this area pins it — signingKey is the deliberate exception, where pinning
// it would rename the table's indexes.)
type tenantPurgeStateSnapshot struct {
	ID uint `gorm:"primarykey"`

	// PurgeState is "active" | "purging" | "purged". Defaulted in the database rather
	// than only in Go, so the rows that already exist when this runs come out `active`
	// instead of empty — a NULL here would read as "not active" to any check written
	// the obvious way.
	PurgeState string `gorm:"not null;default:'active';size:16;index"`

	// PurgeEpoch is when the cut happened, and it is the reason a purge needs more than
	// a boolean. The token is the ONLY tenant identity in the data plane, so a fence
	// keyed by token alone would suppress a future successor's legitimate writes, and a
	// deletion record keyed by token alone becomes ambiguous the moment the token is
	// reused. Every fence and record keys on (token, epoch).
	PurgeEpoch *time.Time
}

func (tenantPurgeStateSnapshot) TableName() string { return "iam_tenants" }

// NewTenantPurgeStateMigration adds the ADR-077 lifecycle columns.
//
// Re-runnable by construction: AutoMigrate adds only what is missing and drops
// nothing, which matters because migrations run with UseTransaction:false and replay
// from the top after a failure.
func NewTenantPurgeStateMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260803120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantPurgeStateSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, column := range []string{"purge_state", "purge_epoch"} {
				if err := tx.Migrator().DropColumn(&tenantPurgeStateSnapshot{}, column); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
