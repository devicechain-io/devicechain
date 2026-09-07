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

// NewQueuedDeviceIndexSchema adds the index behind the dispatch nudge's read
// (Api.QueuedCommandsForDevice): one device's still-queued commands, oldest first.
//
// The query is `tenant_id = ? AND device_token = ? AND status = 'QUEUED' ORDER BY id ASC
// LIMIT 2`, and the key (tenant_id, device_token, id) serves it end to end: two equality
// columns narrow to one device inside one tenant, and `id` then supplies the ORDER BY as
// an ordered index scan, so the LIMIT stops the scan rather than truncating a sorted
// result. That LIMIT is small and the read runs once per created command — on a nudge
// worker, never on the enqueue path itself, which is the point of the queue: CreateCommand
// hands over a device and returns without waiting for any of this — so the difference between stopping after two index entries and sorting a
// device's history is the difference between a nudge that is free and one that taxes every
// create.
//
// 🔴 NOTHING EXISTING COVERS THIS SHAPE, and the three near misses each fail differently:
//
//   - idx_commands_drainable is keyed (tenant_id, device_token, id) — the right key — but
//     partial on HELD ∪ PARKED, so QUEUED is not in its predicate AT ALL. It cannot answer
//     this query for any row.
//   - idx_commands_dispatchable_status is keyed (status, id) and carries neither tenant_id
//     nor device_token, so it would hand this read every tenant's queued commands to
//     filter down to one device's.
//   - the baseline's single-column device_token index finds the device and then leaves
//     status, the tenant predicate and the sort to be done over that device's ENTIRE
//     command history — the one set that grows without bound for a device that has been
//     talking for a year.
//
// 🔴 WIDENING idx_commands_drainable'S PREDICATE TO INCLUDE QUEUED IS THE WRONG FIX, AND
// IT WOULD REINTRODUCE A DEFECT THAT WAS ALREADY REMOVED ONCE. status is not in that
// index's KEY, so a drain reading it would walk queued rows in id order and discard each
// one on a heap recheck — which is exactly the shape NewDispatchableStatusIndexSchema was
// written to eliminate, and it would get worse in proportion to the backlog the presence
// gate exists to let accumulate. A separate index whose predicate is a single status keeps
// both readers on a partition containing only rows they want.
//
// 🔴 THE PARTIAL PREDICATE IS THE STATUS, AND status IS THEREFORE ABSENT FROM THE KEY.
// `status = 'QUEUED'` is a constant, so it is baked into the predicate and every row in
// this index is one the nudge may act on; putting it in the key as well would add a column
// whose value is the same in every entry. deleted_at IS NULL is in the predicate for the
// reason every partial index on this table has it: Command is soft-deleted, so gorm appends
// that clause to every query, and a column neither in the key nor in the predicate forces a
// heap recheck per row.
//
// 🔑 IT IS A SMALL INDEX AND IT STAYS SMALL. QUEUED is the transient state — a row leaves
// it within a tick on a healthy instance — so this covers the arrival rate, not the fleet
// and not the history. It is the same argument that lets PendingCommands read the QUEUED
// partition uncapped.
//
// 🔴 NOT `CREATE INDEX CONCURRENTLY`. Migrations run with UseTransaction:false and replay
// from the top after a failure, and a concurrent build that fails leaves an INVALID index
// behind — which IF NOT EXISTS then treats as present, so the replay skips it and the index
// is never built while every boot reports success. A blocking build on a table whose writes
// are already serialized behind the migration lock costs a short lock once; the concurrent
// form costs a silently missing index forever.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT. What it touches is an index over three
// named columns and one literal status value, all written out below — that IS the snapshot,
// complete and frozen, and unlike a struct it cannot be rewritten by a later change to the
// live model. The table is named schema-qualified and literally for the same reason: an
// index name and its target are SCHEMA, so neither may be computed. (NewTenantStatusIndexSchema
// spells the full reasoning out, including why a gorm type mapping to `commands` would bypass
// the area TablePrefix.)
//
// It is individually re-runnable — IF NOT EXISTS makes a replay a no-op, which the
// UseTransaction:false doctrine requires.
func NewQueuedDeviceIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260907120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_queued_device ` +
				`ON "command-delivery".commands (tenant_id, device_token, id) ` +
				`WHERE deleted_at IS NULL AND status = 'QUEUED';`).Error
		},
		// Losing the index leaves the table correct: the nudge reads the same rows in the
		// same order, it just reads more of the table to do it — and the sweep, which owns
		// delivery, does not use this index at all. Individually re-runnable in this
		// direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_queued_device;`).Error
		},
	}
}
