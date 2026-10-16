// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewNotificationStateCycleSchema adds cycle_raised_at to notification_states: the
// RaisedTime of the alarm cycle a state row's per-cycle fields describe. An alarm token
// is reused when the alarm re-raises, so without it a second cycle inherited the first
// cycle's acknowledgement and clear and was never escalated.
//
// This is an APPENDED migration; the baseline is frozen. The DDL is literal — that is the
// snapshot — because the column goes onto a table that already exists, and a second Go
// shape for notification_states would only be a way to make gorm guess.
//
// The column is NULLABLE with no default and there is no backfill: NULL is the true state
// of a row written before cycles were tracked ("cycle unknown"), and the service adopts
// the cycle the next event names rather than inventing one here.
//
// Re-runnable: IF NOT EXISTS, since migrations run with UseTransaction:false and replay
// from the top after a failure.
func NewNotificationStateCycleSchema() *gormigrate.Migration {
	const states = `"notification-management".notification_states`
	return &gormigrate.Migration{
		ID: "20261016000200",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states +
				` ADD COLUMN IF NOT EXISTS cycle_raised_at timestamp with time zone;`).Error
		},
		// The column holds derived bookkeeping, not configuration: dropping it loses only
		// the ordering of cycles, which the service re-learns from the next event.
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states + ` DROP COLUMN IF EXISTS cycle_raised_at;`).Error
		},
	}
}
