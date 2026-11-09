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
// It also adds the two per-cycle columns the clear notification rests on, both on
// notification_states and both NULLABLE with no default (NULL is the true state of every row
// that predates them: nothing recorded, nothing announced):
//
//   - clear_notified_at: when this cycle's clear was claimed for announcement. The claim is a
//     compare-and-set on it being NULL, so each clear is sent once across replicas and
//     redeliveries, separately from cleared_at (which stops escalation and must be stamped first).
//   - paged_targets: a JSON array of the (channel, recipients) targets that actually delivered
//     this cycle, so a clear goes to where the alarm was paged and nowhere else.
//
// Re-runnable: IF NOT EXISTS, since migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewNotifyOnClearSchema() *gormigrate.Migration {
	const policies = `"notification-management".notification_policies`
	return &gormigrate.Migration{
		ID: "20261109000100",
		Migrate: func(tx *gorm.DB) error {
			const states = `"notification-management".notification_states`
			for _, stmt := range []string{
				`ALTER TABLE ` + policies + ` ADD COLUMN IF NOT EXISTS notify_on_clear boolean NOT NULL DEFAULT false;`,
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS clear_notified_at timestamp with time zone;`,
				`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS paged_targets jsonb;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// The column is configuration; a rollback exists to undo a failed forward migration,
		// not to erase what operators set after it, so it is left in place.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
