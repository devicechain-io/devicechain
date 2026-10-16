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

// NewCommandScopeAnchorsSchema adds commands.scope_anchors: the anchors of the target device
// ("customer:acme", one per tracked relationship) at the moment the command was enqueued
// (ADR-081). A member whose access is limited to part of the tenant sees a command when one
// of them is in its scope.
//
// NULLABLE with no default, and NULL is the honest reading of every existing row: nobody
// recorded who owned the device when it was sent. Such a row is invisible to every scoped
// member and unchanged for everyone else. Pre-GA, nothing is backfilled.
//
// The GIN index serves the scope predicate (`scope_anchors && ARRAY[...]::text[]`), the one
// read that consults the column. Unscoped reads never touch it.
//
// Re-runnable, as migrations run with UseTransaction:false and replay from the top after a
// failure. No snapshot struct: a column added to an existing table is literal,
// schema-qualified DDL, for the reason NewCommandDispatchNonceSchema gives.
func NewCommandScopeAnchorsSchema() *gormigrate.Migration {
	statements := []string{
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS scope_anchors text[];`,
		`CREATE INDEX IF NOT EXISTS idx_commands_scope_anchors ` +
			`ON "command-delivery".commands USING gin (scope_anchors);`,
	}
	return &gormigrate.Migration{
		ID: "20261016000100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Only the index is dropped; the column is left, as for every column this area adds.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_scope_anchors;`).Error
		},
	}
}
