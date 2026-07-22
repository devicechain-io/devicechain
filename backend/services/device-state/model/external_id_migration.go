// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewExternalIdColumn adds external_id to device_states (ADR-067 SP4b): the
// reporting device's external id, denormalized from device-management at resolve
// time (mirroring the ADR-016 unit/dataType denormalization) so the Sparkplug
// adapter's failover reconciliation can enumerate a tenant's asserted-active
// devices by their transport-native "{group}/{node}[/{device}]" identity without a
// hop back into device-management. Nullable (an INFERRED device may have no external
// id). Raw ALTER with IF NOT EXISTS, no snapshot struct, so it can never drift onto
// the live model.
func NewExternalIdColumn() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260722130000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`ADD COLUMN IF NOT EXISTS external_id text;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`DROP COLUMN IF EXISTS external_id;`).Error
		},
	}
}

// NewSourceColumn adds source to device_states (ADR-067 SP4b): the event Source that
// last drove the device (e.g. "sparkplug:{hostId}"), denormalized so failover
// reconciliation scopes its asserted-active enumeration to its OWN source — two
// Sparkplug sources on one tenant (distinct brokers) can then never cross-disconnect
// each other's devices. Nullable. Raw ALTER with IF NOT EXISTS, no snapshot struct.
func NewSourceColumn() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260722140000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`ADD COLUMN IF NOT EXISTS source text;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".device_states ` +
				`DROP COLUMN IF EXISTS source;`).Error
		},
	}
}
