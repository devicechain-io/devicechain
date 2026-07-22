// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewPresenceColumns adds the authoritative-presence projection columns (ADR-067
// S2) to device_states: presence_source (INFERRED|ASSERTED, defaulting INFERRED so
// every existing device keeps today's activity-inferred behavior), and the
// last-applied transition's ordering key session_id (a producer's host-observed
// connect epoch) + presence_time. session_id is bigint (the UnixNano epochs the
// Sparkplug adapter mints fit int64); presence_time is nullable (no transition
// applied yet). Raw ALTER with IF NOT EXISTS, mirroring the binding-columns
// migration — no snapshot struct, so it can never drift onto the live model.
func NewPresenceColumns() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260722120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`ADD COLUMN IF NOT EXISTS presence_source varchar(16) NOT NULL DEFAULT 'INFERRED', ` +
				`ADD COLUMN IF NOT EXISTS session_id bigint NOT NULL DEFAULT 0, ` +
				`ADD COLUMN IF NOT EXISTS presence_time timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`DROP COLUMN IF EXISTS presence_source, ` +
				`DROP COLUMN IF EXISTS session_id, ` +
				`DROP COLUMN IF EXISTS presence_time;`).Error
		},
	}
}
