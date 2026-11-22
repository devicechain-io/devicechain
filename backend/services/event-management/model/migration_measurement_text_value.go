// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewMeasurementTextValueSchema adds measurement_events.text_value, where a STRING reading's
// value is stored. The numeric value column stays the one every aggregate reads; a STRING
// reading leaves it NULL and carries its text here.
//
// It follows NewLocationFixFieldsSchema, and the three properties that migration spells out
// apply unchanged:
//
//  1. Re-runnable: ADD COLUMN IF NOT EXISTS, because this area's DDL is not transactional and
//     a half-applied migration replays from the top.
//  2. Its own literal type, never read from the live model, and the baseline's snapshot
//     struct is left without the column, so a fresh install appends it last, exactly where an
//     upgraded database has it.
//  3. Nullable with NO default. A default lives in a catalog value that a logical dump and
//     restore loses on compressed chunks; nullable is metadata-only on a compressed
//     hypertable, and a pod from before this release, which never names the column, writes
//     the same rows it always did. No backfill: old rows have no text because nothing wrote
//     any.
//
// The column is unused until device-management starts emitting STRING readings, which a later
// release does once every event-management pod can store them.
func NewMeasurementTextValueSchema() *gormigrate.Migration {
	const table = `"event-management"."measurement_events"`

	return &gormigrate.Migration{
		ID: "20261122000000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(fmt.Sprintf(
				`ALTER TABLE %s ADD COLUMN IF NOT EXISTS text_value text;`, table)).Error; err != nil {
				return fmt.Errorf("add measurement_events.text_value: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(fmt.Sprintf(
				`ALTER TABLE %s DROP COLUMN IF EXISTS text_value;`, table)).Error; err != nil {
				return fmt.Errorf("drop measurement_events.text_value: %w", err)
			}
			return nil
		},
	}
}
