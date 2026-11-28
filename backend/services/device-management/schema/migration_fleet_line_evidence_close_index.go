// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewFleetLineEvidenceCloseIndexSchema indexes the blockers both settlement close
// checks probe by tenant and operation. Its predicate mirrors fleetEvidenceCompleteSQL:
// TARGET_ERASED is excluded only from the AMBIGUOUS arm, not from NULL settlements.
// The unsettled index excludes AMBIGUOUS lines and requires evidence/redaction
// conditions the close check does not carry; the admission evidence-open index
// excludes TARGET_ERASED from both arms and is keyed only by tenant.
//
// This literal DDL is the frozen snapshot for this index-only migration. IF NOT
// EXISTS makes replay safe with UseTransaction:false. Use a plain build: a failed
// concurrent build could leave an invalid index that IF NOT EXISTS would skip.
func NewFleetLineEvidenceCloseIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20270102120200",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS ix_fleet_operation_lines_evidence_close ` +
				`ON "device-management".fleet_operation_lines (tenant_id, operation_id) ` +
				`WHERE posting = 'POSTED' AND (settlement IS NULL OR (settlement = 'AMBIGUOUS' AND code <> 'TARGET_ERASED'));`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".ix_fleet_operation_lines_evidence_close;`).Error
		},
	}
}
