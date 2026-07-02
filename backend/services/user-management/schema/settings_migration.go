// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/settings"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewSettingsSchema creates the system_settings table (ADR-042 P2): the
// instance-global key/JSON override store. Defaults live in code and the table
// holds only overrides, so there is no seed here — the table starts empty and is
// populated the first time an admin overrides a setting. Additive.
func NewSettingsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260702170000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&settings.SystemSetting{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"system_settings"})
		},
	}
}
