// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewNotifyOnClearSinceSchema adds notify_on_clear_since to notification_policies: when a policy's
// notify_on_clear last turned on, which is the cut-off the clear pickup measures against. The
// policy's updated_at is the wrong clock for it, because it moves on any edit.
//
// An APPENDED migration; the DDL is literal, which is the snapshot, because the column goes onto
// a table that already exists. The column is NULLABLE with no default: NULL is the true state of
// a policy that has the option off, and of a row written by a pod from before this column (the
// reader falls back to updated_at for it), so a rolling upgrade's old pods still insert and
// update cleanly.
//
// Policies that already have the option on get their updated_at as the stamp: it is the best
// answer to "since when" that is known, and it is what they were measured against until now.
//
// Re-runnable: IF NOT EXISTS, and the backfill only touches rows still NULL.
func NewNotifyOnClearSinceSchema() *gormigrate.Migration {
	const policies = `"notification-management".notification_policies`
	return &gormigrate.Migration{
		ID: "20261110000100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ` + policies + ` ADD COLUMN IF NOT EXISTS notify_on_clear_since timestamp with time zone;`,
				`UPDATE ` + policies + ` SET notify_on_clear_since = updated_at WHERE notify_on_clear AND notify_on_clear_since IS NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Configuration bookkeeping; left in place on rollback like the column it follows.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
