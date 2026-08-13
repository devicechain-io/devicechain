// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, for the reason stated in
// baseline.go: a migration that can reach a live model is a migration whose output
// changes when that model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantStatusIndexSchema adds the composite (tenant_id, status) index on commands.
//
// It is what makes the held-command ceiling cheap. The ceiling counts one tenant's
// withheld commands on every enqueue —
// COUNT(*) WHERE tenant_id = ? AND status = 'HELD' — on the hot path that every REACT
// send-command and every console issue runs through. The baseline's two single-column
// indexes cannot serve that well: tenant_id alone selects the tenant's ENTIRE command
// history (an instance's largest table, and the busiest tenants have the most rows to
// walk), and status alone selects every held command on the INSTANCE, which is the one
// set guaranteed to be large exactly when a fleet is offline and the ceiling matters.
//
// 🔴 IT IS PARTIAL ON deleted_at IS NULL, and that is what makes the count an
// index-only scan rather than an index scan plus a heap fetch per row. Command is
// soft-deleted, so gorm appends `deleted_at IS NULL` to every query; a column that
// is neither in the index nor in its predicate has to be rechecked against the
// heap. Without the WHERE clause a tenant sitting AT its ceiling would pay ~10,000
// heap fetches on every enqueue — precisely under the load the ceiling exists to
// bound. The table's own uix_commands_tenant_token is partial for the same reason.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT, and that is deliberate rather than an
// omission of the rule. What it touches is an index over two named columns, and both
// names are written literally in the DDL below — that IS its snapshot, complete and
// frozen. A struct would be actively worse here for two reasons: gorm derives the table
// name from the TYPE name, so a second type in this package that maps to "commands" is
// impossible without a TableName method, and a TableName method bypasses the area
// TablePrefix (see baseline_snapshot.go) — the migration would then create its index
// against an unqualified table resolved through search_path. And AutoMigrate on a
// snapshot of the whole row would re-assert columns this change has no business
// touching, which is how a narrow migration silently becomes a wide one.
//
// The table is named schema-qualified and literally, matching device-state's appended
// migration: an index name and its target are SCHEMA, so neither may be computed by a
// helper another module could change.
//
// It is individually re-runnable, which it has to be: migrations run with
// UseTransaction:false, so a half-applied migration is never rolled back and replays
// from the top on the next boot. IF NOT EXISTS is what makes the replay a no-op.
//
// tenant_id LEADS the index. The count always fixes a tenant and then a status, so the
// tenant is the equality column that narrows first; leading with status would put every
// tenant's held commands in one contiguous run and make one tenant's backlog the thing
// every other tenant's enqueue has to scan past.
// It also drops the baseline's single-column tenant_id index, which this change
// makes strictly redundant: (tenant_id) is a leading-column prefix of
// (tenant_id, status), so every plan that could use the old one can use the new
// one. `commands` is this area's highest-write table — insert, MarkSent,
// MarkResponse and expiry are four writes over one command's life — and each of
// those maintained a btree that now earns nothing. Taking it here is deliberate:
// the redundancy only comes into existence with this migration, and the drop is
// free while the migration is still unshipped.
//
// The second index below is for the DELIVERY SWEEP, not the ceiling, and it is a
// different shape because the query is: PendingCommands reads
// `status IN ('QUEUED','HELD') ORDER BY id`, cross-tenant, every 30 seconds. A
// ScalarArrayOp against the baseline's single-column status index yields unordered
// output, so the planner adds a Sort over the whole result — and the whole result
// is now the entire held backlog, which is exactly what HELD was created to let
// accumulate. Indexing (id) under a partial predicate gives an ordered index scan
// with no Sort, and the index stays small because it only ever covers rows that
// are still dispatchable.
func NewTenantStatusIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260813000000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_tenant_status ` +
				`ON "command-delivery".commands (tenant_id, status) WHERE deleted_at IS NULL;`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_dispatchable ` +
				`ON "command-delivery".commands (id) ` +
				`WHERE deleted_at IS NULL AND status IN ('QUEUED', 'HELD');`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery"."idx_command-delivery_commands_tenant_id";`).Error
		},
		// Rollback restores the dropped index and removes the two added ones. It is
		// individually re-runnable in both directions, which the UseTransaction:false
		// doctrine requires: a half-applied migration is never rolled back and replays
		// from the top on the next boot.
		//
		// Losing either added index leaves the table intact — the ceiling and the sweep
		// still compute the same answers, they just read more of the table to do it.
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS "idx_command-delivery_commands_tenant_id" ` +
				`ON "command-delivery".commands (tenant_id);`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_dispatchable;`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_tenant_status;`).Error
		},
	}
}
