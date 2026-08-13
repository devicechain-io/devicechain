// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDispatchableStatusIndexSchema re-keys the dispatchable index on (status, id).
//
// 🔴 THE INDEX IT REPLACES WAS CORRECT WHEN IT WAS WRITTEN AND IS WRONG NOW, AND THE
// THING THAT CHANGED IS IN THIS SAME RELEASE. `idx_commands_dispatchable` is keyed on
// (id) alone under a partial predicate covering QUEUED ∪ HELD. That was exactly right
// while the sweep read the WHOLE predicate — one ordered scan, no sort, nothing skipped.
//
// Two changes since have pulled it apart, and only together do they hurt:
//
//  1. the sweep narrowed to QUEUED alone, so it now reads a SUBSET of the predicate; and
//  2. the presence gate started writing HELD, so the other subset — which was empty by
//     construction, because nothing could produce it — is now where an offline fleet's
//     backlog accumulates and sits for days.
//
// `status` is not in the index key, so the planner cannot discriminate: it walks the
// index in id order and applies `status = 'QUEUED'` as a heap filter. Every held row
// therefore costs a heap fetch on EVERY sweep tick, forever, to be discarded. A tenant
// with a ten-thousand-command backlog buys ten thousand pointless heap fetches every
// thirty seconds, and the bigger the backlog the gate is doing its job on, the worse it
// gets. The delivery sweep is slowest exactly when the fleet is most offline.
//
// Leading with `status` makes both readers a direct seek into their own partition, each
// still ordered by id with no sort: the sweep touches no held rows at all, and the
// reconciler's cursored walk (`status = 'HELD' AND id > ?`) touches no queued ones. The
// partial predicate is unchanged, so the index still covers only rows still in play.
//
// 🔑 The lesson is worth more than the index: a correct decision goes stale when a
// different part of the system changes. Nothing was wrong with the old index or the
// reasoning behind it — the workload it was measured against stopped existing.
func NewDispatchableStatusIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260813120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_dispatchable_status ` +
				`ON "command-delivery".commands (status, id) ` +
				`WHERE deleted_at IS NULL AND status IN ('QUEUED', 'HELD');`).Error; err != nil {
				return err
			}
			// Dropped only after the replacement exists, so a failure between the two
			// leaves the table over-indexed rather than under-indexed. Migrations run
			// with UseTransaction:false and replay from the top, so this ordering is the
			// only thing making the intermediate state a safe one to be interrupted in.
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_dispatchable;`).Error
		},
		// Rollback restores the (id) index and removes the (status, id) one, in the same
		// create-then-drop order and for the same reason. Both directions are
		// individually re-runnable.
		//
		// Losing either index leaves the table correct — both queries return the same
		// rows, they just read more to do it.
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_dispatchable ` +
				`ON "command-delivery".commands (id) ` +
				`WHERE deleted_at IS NULL AND status IN ('QUEUED', 'HELD');`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_dispatchable_status;`).Error
		},
	}
}
