// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewFleetLineUnsettledIndexSchema adds the index the bulk-change reconciler lag reads: an
// operation's lines applied, awaiting evidence and holding no verdict yet.
//
// The lag measurement reads, across the instance, every posted line with no evidence, no redaction
// and no settlement, and probes the report log once for each. The indexes on those lines also hold
// every line a deadline settled AMBIGUOUS, which stays awaiting evidence until a late report or
// retention removes it, so they grow with every offline device of every retained change; and with
// few operations the planner joins from the operations and intersects two of them per operation,
// reading that whole backlog. This index holds only the unsettled lines, keyed by operation, so
// each operation's probe reads exactly its unsettled lines, which the deadline keeps few.
//
// 🔴 NO SNAPSHOT STRUCT, by this area's convention for index-only migrations (see
// NewListOrderIndexesSchema and NewFleetLineTenantAwaitingIndexSchema): the DDL below names every
// column literally and IS the snapshot. Re-runnable (IF NOT EXISTS; migrations run with
// UseTransaction:false and replay from the top) and not CONCURRENTLY, for the reason
// NewListOrderIndexesSchema gives: a failed concurrent build leaves an INVALID index that the
// replay's IF NOT EXISTS then skips forever. The plain build holds the lines table's write lock
// while it reads the table once; no release before this one has served bulk changes.
func NewFleetLineUnsettledIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20270102115900",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS ix_fleet_operation_lines_unsettled ` +
				`ON "device-management".fleet_operation_lines (operation_id) ` +
				`WHERE posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL AND settlement IS NULL;`).Error
		},
		// Dropping it leaves every row in place and every read answering the same; the lag
		// measurement just reads the AMBIGUOUS backlog again. Re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".ix_fleet_operation_lines_unsettled;`).Error
		},
	}
}
