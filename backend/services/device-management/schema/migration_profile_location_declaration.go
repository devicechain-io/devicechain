// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewProfileLocationDeclarationSchema adds a device profile's position declaration
// (ADR-078): whether devices built on the profile report their own position, and what
// an operator should expect of it.
//
// This is an APPENDED migration, not a baseline edit — the baseline is frozen, so the
// column lands here and its snapshot struct (deviceProfile, in baseline_snapshot_2.go)
// stays exactly as it was. That is what keeps a fresh install and an upgraded one
// physically identical: both create device_profiles without this column and then add
// it last, in the same position, which is what the golden schema diff compares.
//
// # Why one JSONB column and not several typed ones
//
// The declaration is SINGULAR and NULLABLE by design — a device reports its own
// position once, so there is nothing for a second row to key on, and NULL is a real
// answer meaning "this kind of device does not report position". A spread of typed
// columns (expected_accuracy_meters, expected_update_interval_seconds, …) would
// express the fields but not the DECLARATION: with every column nullable, a profile
// that declares position without stating any expectation is byte-for-byte identical to
// one that declares nothing at all, and telling them apart would need a separate
// boolean whose only job is to disambiguate the other columns. One nullable document
// carries the distinction natively — NULL is undeclared, `{}` is declared and
// unconfigured — and it is also the reservation for the coordinate-system metadata
// (local / indoor / industrial frames) the declaration is expected to grow, which
// lands additively with no further migration.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so
// this must be individually re-runnable: ADD COLUMN IF NOT EXISTS, one statement, no
// dependence on anything before it.
//
// NO DEFAULT, deliberately, for the reason the event-management location-fix migration
// records at length: `ADD COLUMN ... DEFAULT <const>` stores the value in PostgreSQL's
// attmissingval catalog state rather than in the rows, and a logical dump/restore —
// which is what this repo's restore drills do — silently loses it. Nullable with no
// default is both the cheap form and the only safe one. There is no backfill either,
// and none is wanted: an existing profile has no declaration because nobody has
// declared one, and NULL says exactly that.
func NewProfileLocationDeclarationSchema() *gormigrate.Migration {
	const table = `"device-management"."device_profiles"`

	return &gormigrate.Migration{
		ID: "20260809120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table +
				` ADD COLUMN IF NOT EXISTS location_declaration jsonb;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table +
				` DROP COLUMN IF EXISTS location_declaration;`).Error
		},
	}
}
