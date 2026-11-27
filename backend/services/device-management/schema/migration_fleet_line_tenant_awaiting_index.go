// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewFleetLineTenantAwaitingIndexSchema adds the index the bulk-change settlement sweep pages by:
// one tenant's lines still awaiting evidence, in id order.
//
// The sweep (and every pass that is not the fast path) reads a tenant's awaiting lines a thousand
// at a time, keyset on id: `l.tenant_id = ? AND l.id > ? AND posting = 'POSTED' AND
// evidence_revision IS NULL AND redacted_at IS NULL ORDER BY l.id LIMIT 1000`. The only index on
// that predicate, idx_fleet_operation_lines_awaiting_evidence, is keyed on target_id (the fast
// path joins it from the devices that reported), so a page could not stop after a thousand rows:
// each one read EVERY awaiting line of the tenant, through each of its operations, and sorted
// them to keep the first thousand. A sweep of N awaiting lines did that N/1000 times. Lines a
// deadline settled AMBIGUOUS stay awaiting until evidence or retention removes them, so N is
// the tenant's whole open backlog, which admission allows to reach 100,000.
//
// Keyed (tenant_id, id) and partial on exactly the three conditions the read carries, so a page
// reads the index entries it returns and the index holds only awaiting lines.
//
// 🔴 NO SNAPSHOT STRUCT, deliberately, by this area's convention for index-only migrations, copied
// from NewListOrderIndexesSchema (which states it in full) as NewDecoderHealthWindowIndexesSchema
// and NewDeviceDisabledIndexSchema also follow it: one index over named columns, every name
// written literally in the DDL below, which IS the snapshot, complete and frozen. A struct would
// need a TableName() that bypasses this area's table prefix, and AutoMigrate on it would
// re-assert columns this change has no business touching. Re-runnable (IF NOT EXISTS; migrations run with
// UseTransaction:false and replay from the top) and not CONCURRENTLY, for the reason
// NewListOrderIndexesSchema gives: a failed concurrent build leaves an INVALID index that the
// replay's IF NOT EXISTS then skips forever. The plain build holds the table's write lock for its
// length. No release before this one has served bulk changes, so on every instance this upgrades
// the table holds at most what an unserved build's tests left in it, and the build is short.
func NewFleetLineTenantAwaitingIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261231120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS ix_fleet_operation_lines_tenant_awaiting ` +
				`ON "device-management".fleet_operation_lines (tenant_id, id) ` +
				`WHERE posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL;`).Error
		},
		// Dropping it leaves every row in place and every read answering the same; the sweep just
		// pages slowly again. Re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".ix_fleet_operation_lines_tenant_awaiting;`).Error
		},
	}
}
