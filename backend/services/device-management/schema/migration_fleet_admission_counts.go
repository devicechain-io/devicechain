// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// fleetOperationPurgedLineCount is the one column NewFleetOperationPurgedLineCountSchema adds to the
// operation header, as it is at this point in time.
type fleetOperationPurgedLineCount struct {
	PurgedLineCount int32 `gorm:"not null;default:0"`
}

// TableName names the existing table this snapshot adds one column to; the area's search_path
// supplies the schema.
func (fleetOperationPurgedLineCount) TableName() string { return "fleet_operations" }

// NewFleetOperationPurgedLineCountSchema adds purged_line_count to the bulk-change operation header:
// how many of the operation's target_count lines retention has deleted, written in the transaction
// that deletes them. With it, the lines an operation holds are read from its header, and admission's
// retained-lines budget is a sum over the headers instead of a count of every retained line under
// the admission locks.
//
// NOT NULL DEFAULT 0, so every existing header reads 0 and a previous-release pod, which neither
// knows nor writes the column, still inserts headers. Neither can make the sum too small: a header
// it leaves at 0 counts every line its target_count names, so an operation a previous-release pod
// is midway through purging reads as holding all of its lines until its purge completes. Re-runnable:
// AutoMigrate adds the column only when it is missing.
func NewFleetOperationPurgedLineCountSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20270102120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&fleetOperationPurgedLineCount{})
		},
		Rollback: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&fleetOperationPurgedLineCount{}, "purged_line_count") {
				return nil
			}
			return tx.Migrator().DropColumn(&fleetOperationPurgedLineCount{}, "purged_line_count")
		},
	}
}

// NewFleetLineEvidenceOpenIndexSchema adds the index admission counts a tenant's (and the
// instance's) evidence-open lines along: posted lines with no verdict, or an AMBIGUOUS one that a
// late report can still change, other than those whose device was erased.
//
// The count runs at every start and resume under the admission locks. No index served its
// AMBIGUOUS arm, so it read every retained line of the tenant, and of the instance for the
// instance-wide budget. Keyed by tenant and partial on exactly the count's predicate, so the count
// reads the index entries it counts.
//
// 🔴 NO SNAPSHOT STRUCT, by this area's convention for index-only migrations (see
// NewListOrderIndexesSchema and NewFleetLineTenantAwaitingIndexSchema): the DDL below names every
// column literally and IS the snapshot. Re-runnable (IF NOT EXISTS) and not CONCURRENTLY, for the
// reason NewListOrderIndexesSchema gives: a failed concurrent build leaves an INVALID index that the
// replay's IF NOT EXISTS then skips forever. The plain build holds the lines table's write lock
// while it reads the table once; bulk changes are in preview, and the table holds at most the
// instance's retained-lines budget.
func NewFleetLineEvidenceOpenIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20270102120100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS ix_fleet_operation_lines_evidence_open ` +
				`ON "device-management".fleet_operation_lines (tenant_id) ` +
				`WHERE posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') AND code <> 'TARGET_ERASED';`).Error
		},
		// Dropping it leaves every row in place and every count answering the same; admission
		// just reads every retained line again. Re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".ix_fleet_operation_lines_evidence_open;`).Error
		},
	}
}
