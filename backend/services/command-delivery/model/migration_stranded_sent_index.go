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

// NewStrandedSentIndexSchema adds the index behind the stranded-SENT reconciler's scan
// (Api.StrandedSentCommands): the oldest commands still reading SENT with no outcome.
//
// The query is `status = 'SENT' AND sent_time < ? AND (sent_time, id) > (?, ?)
// ORDER BY sent_time ASC, id ASC LIMIT n`, and the key (sent_time, id) serves it end to
// end: the horizon is a range on the leading column, and id breaks ties so the cursor
// resumes at an exact row rather than at a timestamp several rows might share.
//
// 🔴 SENT_TIME LEADS, AND THAT IS NOT AN INCONSISTENCY WITH ITS (status, id) SIBLING
// EVEN THOUGH IT LOOKS LIKE ONE. Status is CONSTANT inside this index's own partial
// predicate — every row in it reads SENT — so a leading status column would be a column
// with exactly one value, which orders nothing and narrows nothing. Leading with
// sent_time puts the horizon comparison on the first key column, where it can bound the
// scan. Anyone "fixing" this into consistency with idx_commands_dispatchable_status
// would turn an ordered range scan into a full scan of every SENT row.
//
// 🔴 idx_commands_dispatchable_status CANNOT SERVE THIS QUERY, and widening it so that
// it could would be a mistake rather than a shortcut. It is partial on
// `status IN ('QUEUED','HELD')`, so SENT rows are not in it at ALL — this is not a
// near miss on key order but an empty intersection. Widening that predicate to admit
// SENT would push the highest-churn state in the table into the index the delivery
// sweep seeks into: every dispatch would pay an extra index write on the hot path, and
// the sweep's own reads would seek through an index bloated with rows it never wants.
// A separate partial index costs writes only while a row is actually in SENT.
//
// 🔴 THE PARTIAL PREDICATE IS THE STATUS, NOT THE HORIZON. `status = 'SENT'` is a
// constant and bakes in, keeping the index confined to commands currently in flight —
// which is a small and self-draining set, since every exit from SENT removes the row
// from the index. The grace horizon is compared against `now`, which is not constant
// and cannot appear in an index predicate at all; it stays a range condition on the
// leading key column, which is exactly where it is cheapest. deleted_at IS NULL is in
// the predicate for the reason every partial index on this table has it: Command is
// soft-deleted, so gorm appends that clause to every query, and a column that is
// neither in the key nor in the predicate forces a heap recheck per row.
//
// 🔴 NOT `CREATE INDEX CONCURRENTLY`. Migrations run with UseTransaction:false and
// replay from the top after a failure, and a concurrent build that fails leaves an
// INVALID index behind — which IF NOT EXISTS then treats as present, so the replay
// skips it and the index is never built while every boot reports success. A blocking
// build on a table whose writes are already serialized behind the migration lock costs
// a short lock once; the concurrent form costs a silently missing index forever.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT. What it touches is an index over two
// named columns and one literal status value, all written out below — that IS the
// snapshot, complete and frozen, and unlike a struct it cannot be rewritten by a later
// change to the live model. The table is named schema-qualified and literally for the
// same reason: an index name and its target are SCHEMA, so neither may be computed.
//
// It is individually re-runnable — IF NOT EXISTS makes a replay a no-op, which the
// UseTransaction:false doctrine requires.
func NewStrandedSentIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260818120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_stranded_sent ` +
				`ON "command-delivery".commands (sent_time, id) ` +
				`WHERE deleted_at IS NULL AND status = 'SENT';`).Error
		},
		// Losing the index leaves the table correct: the reconciler finds the same rows
		// in the same order, it just reads more of the table to do it. Individually
		// re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_stranded_sent;`).Error
		},
	}
}
