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
// The composite index answers the count from the index alone.
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
func NewTenantStatusIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260813000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_tenant_status ` +
				`ON "command-delivery".commands (tenant_id, status);`).Error
		},
		// Dropping the index leaves the table intact — the ceiling still computes the
		// same answer, it just reads more of the table to do it.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_tenant_status;`).Error
		},
	}
}
