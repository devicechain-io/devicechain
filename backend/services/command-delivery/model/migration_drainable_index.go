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

// NewDrainableIndexSchema adds the index behind the wake drain's read
// (Api.DrainableCommands): one device's still-waiting backlog, oldest first.
//
// The query is `tenant_id = ? AND device_token = ? AND status IN ('HELD','PARKED')
// AND (expires_at IS NULL OR expires_at >= ?) ORDER BY id ASC LIMIT n`, and the key
// (tenant_id, device_token, id) serves it end to end: two equality columns narrow to
// one device inside one tenant, and `id` then supplies the ORDER BY as an ordered
// index scan, so the LIMIT stops the scan instead of truncating a sorted result.
//
// 🔴 NOTHING EXISTING COVERS THIS SHAPE, and the nearest candidate is a trap rather
// than a near miss. idx_commands_dispatchable_status is keyed (status, id) and partial
// on QUEUED ∪ HELD — so PARKED, which is HALF the drain set, is not in the index AT
// ALL. A plan using it would read a device's held rows from the index and its parked
// rows from a heap scan, which is worse than not using it. It also carries neither
// tenant_id nor device_token, so it would hand the drain every tenant's held commands
// to filter. The baseline's single-column device_token index is the other half of the
// same story: it finds the device but leaves status, the tenant predicate and the sort
// to be done afterwards, over that device's ENTIRE command history — which is the one
// set that grows without bound for a device that has been talking for a year.
//
// 🔴 THE PARTIAL PREDICATE IS THE STATUS SET, NOT THE EXPIRY HORIZON, and the
// asymmetry is deliberate. `status IN ('HELD','PARKED')` is a constant, so it can be
// baked into the index and keeps the index confined to rows still waiting on a device
// — a vanishing fraction of the table, and it shrinks again the moment a device
// returns. The expiry horizon is compared against `now`, which is not constant and
// cannot appear in an index predicate at all; it stays a filter on the rows the index
// already narrowed to, which is cheap precisely because there are so few of them.
// deleted_at IS NULL is in the predicate for the reason every partial index in this
// table has it: Command is soft-deleted, so gorm appends that clause to every query,
// and a column that is neither in the key nor in the predicate forces a heap recheck
// per row.
//
// 🔴 NOT `CREATE INDEX CONCURRENTLY`. Migrations run with UseTransaction:false and
// replay from the top after a failure, and a concurrent build that fails leaves an
// INVALID index behind — which IF NOT EXISTS then treats as present, so the replay
// skips it and the index is never built while every boot reports success. A blocking
// build on a table whose writes are already serialized behind the migration lock costs
// a short lock once; the concurrent form costs a silently missing index forever.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT. What it touches is an index over three
// named columns and two literal status values, all written out below — that IS the
// snapshot, complete and frozen, and unlike a struct it cannot be rewritten by a later
// change to the live model. The reasoning is spelled out at length in
// NewTenantStatusIndexSchema, including why a gorm type mapping to `commands` would
// bypass the area TablePrefix. The table is named schema-qualified and literally for the
// same reason: an index name and its target are SCHEMA, so neither may be computed.
//
// It is individually re-runnable — IF NOT EXISTS makes a replay a no-op, which the
// UseTransaction:false doctrine requires.
func NewDrainableIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260815120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_drainable ` +
				`ON "command-delivery".commands (tenant_id, device_token, id) ` +
				`WHERE deleted_at IS NULL AND status IN ('HELD', 'PARKED');`).Error
		},
		// Losing the index leaves the table correct: the drain returns the same rows in
		// the same order, it just reads more of the table to do it. Individually
		// re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_drainable;`).Error
		},
	}
}
