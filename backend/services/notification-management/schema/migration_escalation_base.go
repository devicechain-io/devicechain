// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEscalationBaseSchema appends the nullable notifier-clock page time (ADR-017).
// The literal DDL is the frozen snapshot, as in NewStateSourceSchema. No backfill:
// legacy rows use the previous clock basis and old pods can still write during rolls.
// IF NOT EXISTS makes replay after a partial migration safe.
func NewEscalationBaseSchema() *gormigrate.Migration {
	const states = `"notification-management".notification_states`
	return &gormigrate.Migration{
		ID: "20261114000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states + ` ADD COLUMN IF NOT EXISTS escalation_base_at timestamp with time zone;`).Error
		},
		// Keep additive bookkeeping in place on rollback.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
