// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceTypeCommandChannelSchema adds device_types.command_channel: how the devices of the
// type receive their commands, or NULL for the default channel (publish to the device's subject
// and let the transport the device is connected on deliver it).
//
// # Nullable, no default
//
// NULL has to mean "today's behaviour": it is what every existing type holds after this runs,
// and what a replica of the previous version reads and writes during a rolling upgrade (it does
// not know the column and leaves it alone). A DEFAULT would store a value in the catalog rather
// than the rows, which a logical dump and restore loses (see migration_device_disabled.go).
//
// # Re-runnability
//
// One ADD COLUMN IF NOT EXISTS, on a table named literally rather than through the live model
// for the reason migrations.go gives. No row is touched, so no snapshot struct is needed.
func NewDeviceTypeCommandChannelSchema() *gormigrate.Migration {
	const deviceTypes = `"device-management"."device_types"`

	return &gormigrate.Migration{
		ID: "20261114120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + deviceTypes +
				` ADD COLUMN IF NOT EXISTS command_channel varchar(32);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + deviceTypes +
				` DROP COLUMN IF EXISTS command_channel;`).Error
		},
	}
}
