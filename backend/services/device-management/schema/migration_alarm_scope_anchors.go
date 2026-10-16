// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmScopeAnchorsSchema adds alarms.scope_anchors (ADR-081): the anchors of the
// alarm's device ("customer:acme", one per tracked relationship) stamped when the alarm is
// raised, and the GIN index the scope predicate (`scope_anchors && ARRAY[...]`) reads.
//
// NULL on every existing row, and that is the intended reading rather than a gap: a NULL
// stamp matches no scope, so an alarm raised before this existed is invisible to a member
// whose access is limited to some customers, and visible to everyone else as before.
// Pre-GA, no backfill.
//
// Re-runnable: ADD COLUMN IF NOT EXISTS and CREATE INDEX IF NOT EXISTS, nullable with no
// default (see migration_device_disabled.go for why a default is avoided). No snapshot
// struct: it touches no rows, and names the table literally for the reason migrations.go
// gives. Not CONCURRENTLY, for the reason migration_device_disabled_index.go gives.
func NewAlarmScopeAnchorsSchema() *gormigrate.Migration {
	const alarms = `"device-management"."alarms"`

	return &gormigrate.Migration{
		ID: "20261016000100",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE ` + alarms +
				` ADD COLUMN IF NOT EXISTS scope_anchors text[];`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_alarms_scope_anchors ON ` + alarms +
				` USING gin (scope_anchors);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP INDEX IF EXISTS "device-management".idx_alarms_scope_anchors;`).Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE ` + alarms + ` DROP COLUMN IF EXISTS scope_anchors;`).Error
		},
	}
}
