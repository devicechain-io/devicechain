// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewProfileVersioningSchema adds device-profile versioning (ADR-045 slice c): the
// device_profile_versions table (immutable published snapshots of a profile's whole
// capability set) and the device_profiles.active_version pointer (the published
// version a device resolves). It is additive and carries no data step — pre-GA
// decisive cutover, fresh bring-up assumed: on a fresh install there are no profiles
// to back-fill, and a profile takes effect only once explicitly published (an
// existing profile on a non-fresh upgrade needs a one-time publish). The snapshots
// are inline (frozen) and must produce the same columns as
// model.DeviceProfileVersion / model.DeviceProfile.
func NewProfileVersioningSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705170000",
		Migrate: func(tx *gorm.DB) error {
			// Immutable published version snapshots. The uniqueIndex tags create the
			// per-profile monotonic-version constraint (device_profile_id, version).
			type DeviceProfileVersion struct {
				gorm.Model
				rdb.TenantScoped
				DeviceProfileId uint           `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:1"`
				Version         int32          `gorm:"not null;uniqueIndex:uix_device_profile_versions_profile_version,priority:2"`
				Label           sql.NullString `gorm:"size:128"`
				Description     sql.NullString `gorm:"size:1024"`
				Snapshot        datatypes.JSON `gorm:"not null"`
				PublishedBy     string         `gorm:"size:256"`
			}
			if err := tx.AutoMigrate(&DeviceProfileVersion{}); err != nil {
				return err
			}

			// Add the active-version pointer to device_profiles. AutoMigrate on a
			// partial snapshot adds the missing column without touching existing ones
			// (gorm never drops columns). The struct name maps to device_profiles.
			type DeviceProfile struct {
				gorm.Model
				ActiveVersion sql.NullInt32
			}
			return tx.AutoMigrate(&DeviceProfile{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec("ALTER TABLE device_profiles DROP COLUMN IF EXISTS active_version").Error; err != nil {
				return err
			}
			return tx.Migrator().DropTable("device_profile_versions")
		},
	}
}
