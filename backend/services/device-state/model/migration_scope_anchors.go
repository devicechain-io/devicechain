// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceStateScopeAnchorsSchema adds device_states.scope_anchors and scope_anchors_at
// (ADR-081): a device's owners as the live view last learned them, and the precedence that
// orders the two writers of that stamp (see ownership.go). A device's latest readings and
// position gain no column: they are visible through this one (deviceOwners).
//
// NULL on every existing row, and that is the intended reading rather than a gap: a NULL
// stamp matches no scope, so live state from before this existed is invisible to a member
// whose access is limited to some customers until the device's next event or ownership
// fact stamps it, and visible to everyone else as before. Pre-GA, no backfill.
//
// 🔴 NO INDEX ON scope_anchors, unlike alarms'. device_states is the hottest-updated table
// in the platform — a row rewritten on every event — and a GIN index there would be paid by
// every update that cannot stay on its page, to serve a scoped list that the tenant index
// already narrows to one tenant's devices. Measure before adding one.
//
// Re-runnable: ADD COLUMN IF NOT EXISTS, nullable with no default. No snapshot struct: it
// touches no rows, and names the table literally, as NewLatestLocationSchema does.
func NewDeviceStateScopeAnchorsSchema() *gormigrate.Migration {
	const states = `"device-state".device_states`

	return &gormigrate.Migration{
		ID: "20261016120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states +
				` ADD COLUMN IF NOT EXISTS scope_anchors text[], ADD COLUMN IF NOT EXISTS scope_anchors_at timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + states +
				` DROP COLUMN IF EXISTS scope_anchors, DROP COLUMN IF EXISTS scope_anchors_at;`).Error
		},
	}
}
