// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceDisabledIndexSchema adds the index behind DisabledDevicePage: a tenant's disabled
// devices in id order.
//
// command-delivery walks that list at startup and every few minutes, and a fleet has few
// disabled devices among many. Without an index each walk reads the whole devices table in key
// order and discards almost all of it; with this one it reads only the disabled rows.
//
// The index is partial on the same two conditions the query carries (`disabled_at IS NOT NULL`
// and, because Device is soft-deleted, `deleted_at IS NULL`), and keyed (tenant_id, id): the
// tenant predicate every scoped read adds, then the id the keyset cursor pages by. It stays as
// small as the disabled set. No snapshot struct: it touches an index over named columns and no
// rows, and names the table literally for the reason migrations.go gives.
//
// Not CREATE INDEX CONCURRENTLY: migrations run with UseTransaction:false and replay after a
// failure, and a failed concurrent build leaves an INVALID index that IF NOT EXISTS then treats
// as present. The blocking build is short on a partial index.
func NewDeviceDisabledIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261013140000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_disabled ` +
				`ON "device-management"."devices" (tenant_id, id) ` +
				`WHERE disabled_at IS NOT NULL AND deleted_at IS NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "device-management".idx_devices_disabled;`).Error
		},
	}
}
