// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceTypeIndexSchema adds the index behind the alarm search's device-type facet: a
// tenant's live devices of one type.
//
// An alarm table grouped by device type reads one type's alarms as `originator_id IN (the
// tenant's devices of that type)`, and devices carried no index on device_type_id, so every
// such read scanned the tenant's whole device set. The index is keyed (tenant_id,
// device_type_id) — the tenant predicate every scoped read adds, then the type — and partial on
// `deleted_at IS NULL` because Device is soft-deleted and the read carries that condition. No
// snapshot struct: it touches an index over named columns and no rows, and names the table
// literally for the reason migrations.go gives.
//
// Not CREATE INDEX CONCURRENTLY, for the reason NewDeviceDisabledIndexSchema gives.
func NewDeviceTypeIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261020230000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_device_type ` +
				`ON "device-management"."devices" (tenant_id, device_type_id) ` +
				`WHERE deleted_at IS NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".idx_devices_device_type;`).Error
		},
	}
}
