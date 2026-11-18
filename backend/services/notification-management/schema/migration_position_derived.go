// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewPositionDerivedSchema adds the alarm's position verdict to notification_states: true when
// a geofence contributed to the alarm, false when none did, NULL when not known. History reads
// by a caller without location:read use this cache as a pre-filter, withholding true
// and NULL rows; cached false is verified against device-management at read time.
//
// An APPENDED migration; the DDL is literal, which is the snapshot, as in NewStateSourceSchema.
// The column is NULLABLE with no default and there is no backfill: NULL is the true state of a
// row written before it (the verdict is not known, and such a row is withheld until the alarm
// next changes state), and of a row an old pod writes during a rolling upgrade, so old and new
// pods overlap safely.
//
// Re-runnable: IF NOT EXISTS.
func NewPositionDerivedSchema() *gormigrate.Migration {
	const states = `"notification-management".notification_states`
	return &gormigrate.Migration{
		ID: "20261117000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS position_derived boolean;`).Error
		},
		// Keep the verdict column in place on rollback, like the columns it follows.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
