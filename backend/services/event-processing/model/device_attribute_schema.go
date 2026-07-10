// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceAttributeSchema adds the durable read-model the dynamic-threshold feature needs (ADR-051
// slice 4c-3): DeviceAttribute (the current numeric value of a platform-set device attribute, so a
// detection rule can resolve a threshold from a device's own attribute) plus DeviceAttributeDeletion
// (the per-device resurrection fence a device deletion drops, since the deletion fact carries no
// attribute keys). Both are rebuilt-from at startup so a value survives a restart independent of the
// finite-retention device-attribute fact stream, exactly like the rule/roster projections. This
// slice lands and maintains them; the engine eval that reads them (the CEL "attr" var) is slice
// 4c-3b-2.
func NewDeviceAttributeSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712000000",
		Migrate: func(tx *gorm.DB) error {
			type DeviceAttribute struct {
				Tenant      string    `gorm:"primaryKey;size:256;not null"`
				DeviceToken string    `gorm:"primaryKey;size:256;not null"`
				Scope       string    `gorm:"primaryKey;size:64;not null"`
				AttrKey     string    `gorm:"primaryKey;size:256;not null"`
				Value       float64   `gorm:"not null"`
				Deleted     bool      `gorm:"not null"`
				LastEventAt time.Time `gorm:"not null"`
				UpdatedAt   time.Time
			}
			type DeviceAttributeDeletion struct {
				Tenant      string    `gorm:"primaryKey;size:256;not null"`
				DeviceToken string    `gorm:"primaryKey;size:256;not null"`
				DeletedAt   time.Time `gorm:"not null"`
				UpdatedAt   time.Time
			}
			return tx.AutoMigrate(&DeviceAttribute{}, &DeviceAttributeDeletion{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("device_attribute_deletions"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("device_attributes")
		},
	}
}
