// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDropInactivityColumnsSchema drops device_states.inactivity_timeout and
// device_states.inactivity_alarm_time.
//
// inactivity_timeout was documented as a per-device override of the inactivity window, and
// nothing could ever set it: the service has no mutation that writes a device state, so every
// row held the default and the sweep's per-row CASE always resolved to the platform value. The
// window is now one platform-wide constant. inactivity_alarm_time was written by the sweep
// (to the very instant it also writes to last_disconnect_time) and cleared on reconnect, and
// read by nothing but a GraphQL field no client selected.
//
// Dropping loses nothing: the one value the first column can hold is the default it was
// created with, and the second duplicates last_disconnect_time.
//
// Re-runnable: DROP COLUMN IF EXISTS. No snapshot struct, because it touches no rows and
// names the table literally, as NewDeviceStateScopeAnchorsSchema does. The rollback restores
// the columns as the baseline declared them (the default re-fills every row).
func NewDropInactivityColumnsSchema() *gormigrate.Migration {
	const states = `"device-state".device_states`

	return &gormigrate.Migration{
		ID: "20261104120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states +
				` DROP COLUMN IF EXISTS inactivity_timeout, DROP COLUMN IF EXISTS inactivity_alarm_time;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states +
				` ADD COLUMN IF NOT EXISTS inactivity_alarm_time timestamptz,` +
				` ADD COLUMN IF NOT EXISTS inactivity_timeout bigint NOT NULL DEFAULT 600;`).Error
		},
	}
}
