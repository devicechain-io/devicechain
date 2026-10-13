// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceDisabledSchema adds devices.disabled_at: when an operator disabled the device,
// or NULL while it is enabled. A disabled device's events are refused at resolution, its
// broker connects are refused, and commands to it are refused at enqueue.
//
// # A timestamp, not an `enabled` boolean
//
// NULL has to mean "enabled", because NULL is what every existing device holds after this
// runs and what every reader that has not been told about the column sees: a zero value
// read through a narrow column list, or a cached device encoded by a replica of the
// previous version during a rolling upgrade. An `enabled boolean` would need a DEFAULT to
// say that, and the default is the problem below.
//
// # Nullable, no default
//
// `ADD COLUMN ... DEFAULT <const>` stores the value in PostgreSQL's attmissingval catalog
// state rather than in the rows, which migration_profile_location_declaration.go records
// a logical dump/restore loses. Nullable with no default needs no backfill: an existing
// device has never been disabled, and NULL says exactly that.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: the
// one statement is ADD COLUMN IF NOT EXISTS. No snapshot struct is needed, because the
// migration touches no rows; it names the table literally rather than through the live
// model for the reason migrations.go gives.
func NewDeviceDisabledSchema() *gormigrate.Migration {
	const devices = `"device-management"."devices"`

	return &gormigrate.Migration{
		ID: "20261013120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + devices +
				` ADD COLUMN IF NOT EXISTS disabled_at timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + devices +
				` DROP COLUMN IF EXISTS disabled_at;`).Error
		},
	}
}
