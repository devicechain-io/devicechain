// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDetectReconcileInstantsSchema stores the two instants the detection engine's copy of this
// service's state is ordered on, so that event-processing can reconcile that copy against what
// is stored here and reproduce exactly what a lost notification would have said:
//
//   - device_profiles.active_since — when the profile's active version BECAME active. Publish
//     and rollback both set it, in the transaction that moves active_version. A rollback used to
//     announce the wall clock at the moment of sending and store nothing, so the instant the
//     engine's dead-man grace and its monotonic active-version guard are keyed on could not be
//     reproduced by anyone.
//   - devices.expected_since — when the device's membership of its current profile began, set by
//     a re-type or a re-point of its type. NULL means "since the device was created", and the one
//     reader (rosterEntries in the model) COALESCEs it with created_at.
//
// # Both columns are NULLABLE, with NO DEFAULT and NO BACKFILL — all three on purpose
//
// NOT NULL would break the rolling upgrade. device-management rolls with maxUnavailable 0, so a
// new pod runs this migration while old pods are still serving, and an old pod's INSERT does not
// name either column — NOT NULL with no default turns every device create on an old pod into a
// failed write for the whole overlap (the user-management session-epoch migration records the same
// call). It would also take an ACCESS EXCLUSIVE scan of devices, blocking event resolution, on a
// large fleet.
//
// No DEFAULT, for the attmissingval reason migration_profile_location_declaration.go records: a
// constant default lives in catalog state that a logical dump/restore loses.
//
// No backfill, because NULL already has a meaning each reader resolves in ONE place, so writing
// that meaning into every row would be a second copy of it: expected_since NULL is created_at
// (rosterEntries), and active_since NULL is derived from the version rows by
// activationFloorExpr — the newest version's publish time, plus a microsecond when the active
// version is not the newest (it was rolled back to, so it became active after that publish).
// Both rules survive an old pod writing a row during the overlap, which a one-off backfill would
// not: the backfill runs once, the overlap keeps producing NULLs.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: each
// statement is ADD COLUMN IF NOT EXISTS and depends on nothing before it.
func NewDetectReconcileInstantsSchema() *gormigrate.Migration {
	const profiles = `"device-management"."device_profiles"`
	const devices = `"device-management"."devices"`

	return &gormigrate.Migration{
		ID: "20260926120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE ` + profiles +
				` ADD COLUMN IF NOT EXISTS active_since timestamptz;`).Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE ` + devices +
				` ADD COLUMN IF NOT EXISTS expected_since timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE ` + devices +
				` DROP COLUMN IF EXISTS expected_since;`).Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE ` + profiles +
				` DROP COLUMN IF EXISTS active_since;`).Error
		},
	}
}
