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

// NewDispatchAfterSchema adds the columns a maintenance-window hold is made of.
//
//   - commands.dispatch_after: the earliest instant a claim may take the row. NULL means
//     "no hold", which is every command that predates this and every command that is not
//     gated, so nothing is backfilled. The claim predicates read it, and that is the whole
//     mechanism: whichever road reaches a row (the delivery sweep, the LwM2M wake drain,
//     the live confirm) ends in a conditional UPDATE that refuses a row whose stamp is in
//     the future.
//   - commands.window_token: which maintenance window the stamp came from, so an edit of
//     that window can re-stamp the rows still waiting on it. A token rather than an id
//     because the window is owned by another service.
//   - command_batches.waiting: how many of the batch's admitted members were held for a
//     window when the batch was created. A stored fact about that moment, like resolved and
//     accepted beside it; NULL on a batch from before the column existed.
//
// All three are NULLABLE with no default. A constant default is stored in the catalog
// (attmissingval) rather than in the rows, which a logical dump and restore loses, and NULL
// is the true reading of every existing row.
//
// # The index
//
// idx_commands_window_waiting backs the re-stamp pass's discovery read: which (tenant,
// window) pairs have undelivered rows stamped from them. It is partial on the three
// undelivered statuses, so it holds only the rows that can still be moved and shrinks as
// they go out; those literals are the ones undeliveredStatusStrings() holds.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure; every
// statement is IF NOT EXISTS. No snapshot struct is declared: a column added to an existing
// table is literal, schema-qualified DDL, for the reason NewCommandDispatchNonceSchema
// gives.
func NewDispatchAfterSchema() *gormigrate.Migration {
	statements := []string{
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS dispatch_after timestamptz;`,
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS window_token varchar(128);`,
		`ALTER TABLE "command-delivery".command_batches ADD COLUMN IF NOT EXISTS waiting integer;`,
		`CREATE INDEX IF NOT EXISTS idx_commands_window_waiting ` +
			`ON "command-delivery".commands (tenant_id, window_token) ` +
			`WHERE deleted_at IS NULL AND window_token IS NOT NULL ` +
			`AND status IN ('QUEUED', 'HELD', 'PARKED');`,
	}
	return &gormigrate.Migration{
		ID: "20261014000300",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Only the index is dropped; the nullable columns are left (dropping a column
		// destroys data, and a rollback undoes a failed forward migration rather than
		// erasing what ran after it).
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_window_waiting;`).Error
		},
	}
}
