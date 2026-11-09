// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewStateSourceSchema adds the alarm's source to notification_states: the originator and the
// device's token, name and type token. A timed re-notification and a clear the scheduler picks
// up late are rendered from this row alone, and without these they could not name the device or
// link to it as the event-driven notification does.
//
// An APPENDED migration; the DDL is literal, which is the snapshot, because the columns go onto
// a table that already exists. Every column is NULLABLE with no default and there is no
// backfill: NULL is the true state of a row written before them (the source is not known, and
// the renderers omit it rather than invent it), and of a row an old pod writes during a rolling
// upgrade, so old and new pods overlap safely.
//
// Re-runnable: IF NOT EXISTS on each column.
func NewStateSourceSchema() *gormigrate.Migration {
	const states = `"notification-management".notification_states`
	return &gormigrate.Migration{
		ID: "20261110000200",
		Migrate: func(tx *gorm.DB) error {
			for _, col := range []string{
				"originator_type text",
				"originator_id bigint",
				"device_token text",
				"device_name text",
				"device_type_token text",
			} {
				if err := tx.Exec(`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS ` + col + `;`).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Descriptive bookkeeping left in place on rollback, like the columns it follows.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
