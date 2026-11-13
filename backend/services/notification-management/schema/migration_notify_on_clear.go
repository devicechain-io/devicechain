// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewNotifyOnClearSchema adds notify_on_clear to notification_policies: the opt-in that makes
// a policy tell its channels when an alarm clears, not only when it raises.
//
// This is an APPENDED migration; the baseline is frozen. The DDL is literal, which is the
// snapshot: the column goes onto a table that already exists, and a second Go shape for
// notification_policies would only be a way to make gorm guess.
//
// The column is NOT NULL DEFAULT false. false is what every existing policy did before the
// column existed, so there is no backfill, and the default is what keeps a rolling upgrade
// safe: a pod from before this migration inserts a policy without naming the column and the
// row still lands, as a policy that does not notify on clear.
//
// Re-runnable: IF NOT EXISTS, since migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewNotifyOnClearSchema() *gormigrate.Migration {
	const policies = `"notification-management".notification_policies`
	return &gormigrate.Migration{
		ID: "20261109000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + policies +
				` ADD COLUMN IF NOT EXISTS notify_on_clear boolean NOT NULL DEFAULT false;`).Error
		},
		// The column is configuration; a rollback exists to undo a failed forward migration,
		// not to erase what operators set after it, so it is left in place.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}

// NewClearAnnouncementSchema adds the two per-cycle columns a clear notification rests on, both
// on notification_states and both NULLABLE with no default (NULL is the true state of every row
// that predates them: nothing recorded, nothing announced). It is its own migration, after
// NewNotifyOnClearSchema, because a database that already ran that one must still get them.
//
//   - clear_notified_cycle: the cycle (cycle_raised_at) whose clear has been claimed for
//     announcement. The claim is a compare-and-set on it differing from the row's cycle, so each
//     cycle's clear is sent once across replicas and redeliveries, separately from cleared_at
//     (which stops escalation and is stamped first). It names a CYCLE, never a time, so no clock
//     ordering (a device's skewed event time, another pod's clock) can make a claim re-winnable,
//     and a value left by an earlier cycle never reads as this one's.
//   - clear_notified_at: when the claim was taken. Informational and the give-back key only.
//   - paged_cycle: the cycle paged_targets describes, as a column so the pickup query can match
//     it to the row's cycle in SQL (a row whose paged set belongs to another cycle must not
//     occupy the scan). Also nullable, no default.
//   - a partial index (tenant_id, updated_at) over the rows the pickup reads: stamped cleared
//     and holding a paged set.
//   - paged_targets: a JSON document {cycle, targets} of the (channel, recipients) targets that
//     actually delivered in that cycle, so a clear goes to where the alarm was paged and nowhere
//     else. It names its cycle, so a value left by an earlier cycle (a pod from before this
//     release advances the cycle without knowing the column) is ignored.
//
// Re-runnable: IF NOT EXISTS.
func NewClearAnnouncementSchema() *gormigrate.Migration {
	const states = `"notification-management".notification_states`
	return &gormigrate.Migration{
		ID: "20261109000200",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS clear_notified_at timestamp with time zone;`,
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS clear_notified_cycle timestamp with time zone;`,
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS paged_targets jsonb;`,
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS paged_cycle timestamp with time zone;`,
				`CREATE INDEX IF NOT EXISTS "idx_notification-management_notification_states_pending_clear" ON ` +
					states + ` (tenant_id, updated_at) WHERE cleared_at IS NOT NULL AND paged_targets IS NOT NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Bookkeeping about delivery, re-learned from the next page; left in place on rollback.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
