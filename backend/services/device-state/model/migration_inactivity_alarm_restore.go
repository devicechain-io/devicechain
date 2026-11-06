// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewInactivityAlarmTimeRestoreSchema repairs inactivity_alarm_time for the rows v1.1 pods
// left behind. v1.1 stopped writing the column (and served the deprecated inactivityAlarmTime
// field from last_disconnect_time instead); this release writes it again, as v1.0 did.
//
// Two kinds of row are wrong at that point, and nothing else is touched:
//
//   - an ACTIVE row with a value: v1.0 cleared it when the device came back, v1.1 did not, so
//     the value is a past episode and v1.0 would have shown null.
//   - an INACTIVE INFERRED row with no value but a last_disconnect_time: a v1.1 sweep marked
//     it. The sweep stamps both columns with one instant, so the value is recovered from
//     last_disconnect_time. The same shape is also left by a disconnect report on a device
//     later demoted to INFERRED, which no data distinguishes; the field answers a time for
//     those rows (as v1.1 did) until the device is active again. It is a one-time
//     approximation, bounded to rows written during the v1.1 window.
//
// Data only, no DDL, and individually re-runnable: both statements are idempotent.
func NewInactivityAlarmTimeRestoreSchema() *gormigrate.Migration {
	const states = `"device-state".device_states`

	return &gormigrate.Migration{
		ID: "20261106120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`UPDATE ` + states +
				` SET inactivity_alarm_time = NULL WHERE active AND inactivity_alarm_time IS NOT NULL;`).Error; err != nil {
				return err
			}
			return tx.Exec(`UPDATE ` + states +
				` SET inactivity_alarm_time = last_disconnect_time` +
				` WHERE NOT active AND presence_source <> 'ASSERTED'` +
				` AND inactivity_alarm_time IS NULL AND last_disconnect_time IS NOT NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
