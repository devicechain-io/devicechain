// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewCommandBatchCancelSchema records that a fleet write was called off.
//
// `cancelled_at` is the moment someone pulled the brake; `cancelled_count` is how many
// commands that call caught. Both are stored facts about that moment, which is why they
// are columns rather than a query over the batch's commands: command rows are not
// immortal, and a derived count would sink below the truth as they are purged, with
// nothing recording that it had.
//
// 🔴 THERE IS NO SNAPSHOT STRUCT HERE, AND THAT IS FORCED RATHER THAN CHOSEN. The rule
// this package follows is that a NEW table is created from a snapshot struct while
// columns added to an EXISTING one are literal DDL — see NewCommandBatchSchema, which
// states the reasoning at length. It applies with extra force to this table: gorm derives
// "command_batches" from the type name `commandBatch`, so a second snapshot type in this
// package cannot map to it without a TableName method, and a TableName method bypasses
// the area TablePrefix and would resolve through search_path instead. AutoMigrate over a
// whole-row snapshot would also re-assert every other column, which is how a two-column
// change silently becomes a table-wide one.
//
// Both statements are individually re-runnable, which they have to be: migrations run
// with UseTransaction:false, so a half-applied migration is never rolled back and replays
// from the top on the next boot.
func NewCommandBatchCancelSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260814120000",
		Migrate: func(tx *gorm.DB) error {
			// Both nullable, and deliberately without a default. NULL means "nobody has
			// cancelled this batch", which a zero default could not express: a cancel
			// that legitimately catches nothing writes a stamp with a count of 0, and
			// that is a different fact from never having been cancelled at all.
			for _, ddl := range []string{
				`ALTER TABLE "command-delivery".command_batches ` +
					`ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;`,
				`ALTER TABLE "command-delivery".command_batches ` +
					`ADD COLUMN IF NOT EXISTS cancelled_count integer;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`ALTER TABLE "command-delivery".command_batches ` +
					`DROP COLUMN IF EXISTS cancelled_count;`,
				`ALTER TABLE "command-delivery".command_batches ` +
					`DROP COLUMN IF EXISTS cancelled_at;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
