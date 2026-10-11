// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// publishedVersionColumns is the one column this migration adds to a table the baseline
// already created.
//
// It is raw DDL rather than a snapshot struct, for the reason the other appended
// migrations that touch an existing table record: gorm derives a table name from the TYPE
// name, so a second Go shape mapping to `dashboards` would need a TableName(), and a
// TableName() bypasses this area's TablePrefix. Writing the column and its table out
// literally IS the snapshot here, and it cannot drift toward the live model because
// there is no Go type for the live model to be swapped in for.
//
// The column is NULLABLE with no default, and NULL is the correct reading of every row
// that predates it: a dashboard published before the pointer existed has no pointer. No
// backfill — such a board is served as "not published" until it is published again.
var publishedVersionColumns = []string{
	`ALTER TABLE "dashboard-management".dashboards ADD COLUMN IF NOT EXISTS published_version integer;`,
}

// NewPublishedVersionSchema adds dashboards.published_version: the version number of the
// dashboard_versions row viewers are served, NULL until the first publish.
//
// Re-runnable (IF NOT EXISTS), as migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewPublishedVersionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261010120000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range publishedVersionColumns {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// The column is left in place: dropping one destroys data, and a rollback exists
		// to undo a failed forward migration, not to erase what ran after it.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
