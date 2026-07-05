// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceProfileSchema adds the device_profiles table and the DeviceType columns
// that reference it (ADR-045): the Device Profile is un-fused from the Device Type
// into a distinct, tenant-scoped capability contract a type adopts. This slice is
// additive — the metric/command/alarm definitions still hang off DeviceType and
// relocate onto the profile in a later slice. The snapshots are inline (frozen) and
// must produce the same columns as model.DeviceProfile / model.DeviceType.
func NewDeviceProfileSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705150000",
		Migrate: func(tx *gorm.DB) error {
			type DeviceProfile struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				Category   sql.NullString `gorm:"size:64;index"`
				Provenance sql.NullString `gorm:"size:256"`
			}
			if err := tx.AutoMigrate(&DeviceProfile{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			if err := rdb.CreateTenantTokenIndex(tx, &DeviceProfile{}); err != nil {
				return err
			}

			// Add the reference + identity facets to device_types. AutoMigrate on a
			// partial snapshot adds the missing columns without touching existing
			// ones (gorm never drops columns). The struct name maps to device_types.
			type DeviceType struct {
				gorm.Model
				ProfileId    *uint          `gorm:"index"`
				Manufacturer sql.NullString `gorm:"size:128"`
				ModelName    sql.NullString `gorm:"column:model;size:128"`
			}
			return tx.AutoMigrate(&DeviceType{})
		},
		Rollback: func(tx *gorm.DB) error {
			type DeviceType struct {
				gorm.Model
				ProfileId    *uint
				Manufacturer sql.NullString
				ModelName    sql.NullString `gorm:"column:model"`
			}
			for _, col := range []string{"profile_id", "manufacturer", "model"} {
				if tx.Migrator().HasColumn(&DeviceType{}, col) {
					if err := tx.Migrator().DropColumn(&DeviceType{}, col); err != nil {
						return err
					}
				}
			}
			return tx.Migrator().DropTable("device_profiles")
		},
	}
}
