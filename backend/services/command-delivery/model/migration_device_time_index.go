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

// NewDeviceTimeIndexSchema adds the index behind the command search's time range
// (CommandSearchCriteria.StartTime/EndTime): one device's commands whose time falls in a
// range, where a command's time is when it was sent, or when it was queued if it never was.
// A chart reads it to mark a device's commands on its time axis, for any range up to a month
// back, while that device keeps talking.
//
// The read is `tenant_id = ? AND device_token = ? AND COALESCE(sent_time, queued_time)
// BETWEEN ? AND ?`. The two equality columns narrow to one device inside one tenant, and the
// expression then bounds a range scan, so the rows read are the rows in range. Without it the
// baseline's single-column device_token index finds the device and leaves the range to be
// tested over that device's ENTIRE command history, which grows without bound.
//
// The key holds the EXPRESSION, not sent_time: the search filters on exactly
// COALESCE(sent_time, queued_time), and a predicate on an expression can only use an index on
// the same expression. A command never sent (still queued or held, or expired or cancelled
// first) has no sent_time and would otherwise be unreachable by the range.
//
// deleted_at IS NULL is the partial predicate for the reason every partial index on this
// table has it: Command is soft-deleted, so gorm appends that clause to every query.
//
// 🔴 NOT `CREATE INDEX CONCURRENTLY`, for the reason NewQueuedDeviceIndexSchema gives: a
// concurrent build that fails leaves an INVALID index that IF NOT EXISTS then treats as
// present on the replay.
//
// 🔴 THIS MIGRATION DECLARES NO SNAPSHOT STRUCT. What it touches is an index over named
// columns, all written out below, and the table is named schema-qualified and literally; that
// IS the snapshot, and no later change to the live model can rewrite it.
//
// It is individually re-runnable: IF NOT EXISTS makes a replay a no-op.
func NewDeviceTimeIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261021120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_commands_device_time ` +
				`ON "command-delivery".commands (tenant_id, device_token, (COALESCE(sent_time, queued_time))) ` +
				`WHERE deleted_at IS NULL;`).Error
		},
		// Losing the index leaves the search correct, only slower for a device with a long
		// history. Individually re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_commands_device_time;`).Error
		},
	}
}
