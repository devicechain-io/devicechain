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

// NewCommandOutcomeSchema adds the stored outcome class of a finished command and the
// batch-to-batch retry link.
//
// `commands.outcome` is NULL while a command is live and one of SUCCEEDED / NOT_DELIVERED /
// REJECTED / UNKNOWN once it has finished, written by the very UPDATE that terminates it.
// `command_batches.retry_of` is the token of the batch a retry batch re-attempts.
//
// 🔴 THE BACKFILL IS CONSERVATIVE ON PURPOSE. A row that finished before this column existed
// carries no record of which fact its FAILED recorded (the only evidence was error prose), so
// every historical FAILED is classed UNKNOWN: a retry of it then requires the operator's explicit
// acknowledgement instead of being offered as proven-safe. The statuses with one possible class
// (SUCCESSFUL, EXPIRED, CANCELLED, TIMEOUT) are classed exactly. The backfill only touches rows
// still NULL, so a replay of this migration never overwrites a stamp.
//
// No snapshot struct, by the rule the neighbouring column migrations follow: columns added to an
// EXISTING table are literal, schema-qualified DDL, since AutoMigrate over a whole-row snapshot
// would re-assert every other column. Every statement is individually re-runnable, because
// migrations run with UseTransaction:false and replay from the top after a failure.
func NewCommandOutcomeSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261013120000",
		Migrate: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`ALTER TABLE "command-delivery".commands ` +
					`ADD COLUMN IF NOT EXISTS outcome varchar(16);`,
				`UPDATE "command-delivery".commands SET outcome = CASE status ` +
					`WHEN 'SUCCESSFUL' THEN 'SUCCEEDED' ` +
					`WHEN 'EXPIRED' THEN 'NOT_DELIVERED' ` +
					`WHEN 'CANCELLED' THEN 'NOT_DELIVERED' ` +
					`ELSE 'UNKNOWN' END ` +
					`WHERE outcome IS NULL AND status IN ('SUCCESSFUL','FAILED','TIMEOUT','EXPIRED','CANCELLED');`,
				`ALTER TABLE "command-delivery".command_batches ` +
					`ADD COLUMN IF NOT EXISTS retry_of varchar(128);`,
				`CREATE INDEX IF NOT EXISTS idx_command_batches_retry_of ` +
					`ON "command-delivery".command_batches (tenant_id, retry_of) WHERE retry_of IS NOT NULL;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`DROP INDEX IF EXISTS "command-delivery".idx_command_batches_retry_of;`,
				`ALTER TABLE "command-delivery".command_batches DROP COLUMN IF EXISTS retry_of;`,
				`ALTER TABLE "command-delivery".commands DROP COLUMN IF EXISTS outcome;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
