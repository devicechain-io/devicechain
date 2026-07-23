// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDefaultCommandTtl backfills a terminal horizon onto commands that never had one.
//
// It stamps a 7-day expires_at (168h, the platform default at this migration's
// authoring — a literal snapshot, NOT the live config value, which may drift) onto
// every non-terminal row that carries none. Before this, a command created without an
// explicit expiresAt sat in QUEUED/SENT forever: ExpireStale only touches rows with a
// non-null expires_at, and nothing else moves a command to a terminal state without a
// device response, so a TTL-less command (every REACT send-command) never timed out.
// This is the decisive pre-GA cutover that closes the gap for existing rows;
// CreateCommand stamps new ones going forward. now() is evaluated once, at migration
// time, which is correct for a one-time backfill.
//
// Raw DDL rather than a Migrator call on the live model, matching NewDropDeliveredTime:
// a migration must not resolve its shapes through a struct that can move on without it.
// The table is schema-qualified so an unqualified name run against an unexpected
// search_path cannot no-op and still report success (a migration that cannot fail is
// worse than one that does).
//
// No index is added here on purpose. The wake-time drain (ADR-075 L4b) queries a
// device's SENT commands within its tenant; the initial schema's single-column
// device_token index already makes that per-device lookup selective, and a composite
// (tenant_id, device_token, status) index would diverge from the live model — its
// leading column lives in the shared rdb.TenantScoped embed, which cannot carry a
// service-specific index tag, so migration-diff would flag the migrated schema against
// the AutoMigrated model. A measured composite index is a follow-up if wake-query load
// ever warrants it.
//
// Rollback is a deliberate no-op. The stamp is not cleanly reversible — a backfilled
// expires_at is indistinguishable from a caller-supplied one — and un-recording this
// migration must NOT strip expires_at values that are now legitimately load-bearing
// (they are what lets ExpireStale terminate the row). So rolling the version marker
// back leaves the data as-is, which is the honest behavior; it is present only to
// match the convention every other migration here follows.
func NewDefaultCommandTtl() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260723000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(
				`UPDATE "command-delivery"."commands"
				    SET expires_at = now() + interval '604800 seconds'
				  WHERE expires_at IS NULL
				    AND deleted_at IS NULL
				    AND status IN ('QUEUED', 'SENT');`).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
