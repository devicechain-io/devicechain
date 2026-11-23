// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewLatestMeasurementTextValueSchema adds latest_measurements.text_value, where a STRING
// reading's last value is kept. The numeric value column stays the one a number is read from;
// a STRING reading leaves it NULL and carries its text here.
//
// It follows NewDeviceStateIncarnationSchema, and the properties that migration states apply:
//
//  1. Re-runnable: the column is added only when absent, because migrations run with
//     UseTransaction:false and a half-applied one replays from the top.
//  2. Its own snapshot, never the live model: the struct below names only the column it adds,
//     and the baseline's snapshot is left without it, so a fresh install appends it last,
//     exactly where an upgraded database has it.
//  3. Nullable with NO default and no backfill. A pod from before this release never names the
//     column, so the rows it writes are the rows it always wrote; old rows hold no text
//     because nothing wrote any. The old pods' upsert leaves an existing text_value in place
//     when it overwrites the numeric value, which cannot occur while a metric keeps its data
//     type (see the producer slice in device-management, which is what first writes text).
//
// The column is unused until device-management starts emitting STRING readings.
func NewLatestMeasurementTextValueSchema() *gormigrate.Migration {
	// The name derives latest_measurements through the area's naming strategy, including its
	// schema prefix (see migration_incarnation.go); renaming this type retargets the migration.
	type latestMeasurement struct {
		TextValue *string `gorm:"type:text"`
	}
	return &gormigrate.Migration{
		ID: "20261123120000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&latestMeasurement{}, "TextValue") {
				return nil
			}
			return tx.Migrator().AddColumn(&latestMeasurement{}, "TextValue")
		},
		Rollback: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&latestMeasurement{}, "TextValue") {
				return nil
			}
			return tx.Migrator().DropColumn(&latestMeasurement{}, "TextValue")
		},
	}
}
