// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// InstalledComponentsID is the installed-components migration's frozen ID.
const InstalledComponentsID = "20261123000100"

// deviceInstalledComponent is the snapshot of device_installed_components: the version a device last
// told the platform it was running, per component. It answers "which firmware is the fleet on",
// which no other table can: an attempt records only the update the platform asked for, and an
// inventory report used to be folded into an attempt and then forgotten.
//
// Keyed by device INCARNATION as well as token, so replacement hardware that reuses a token starts
// with nothing and never inherits the old device's versions. There is no foreign key: a device
// deletion fact removes the rows, and a previous-release pod never touches the table.
//
// Self-contained down to the mixins, like every snapshot here, with no TableName method for the
// reason given on artifact.
type deviceInstalledComponent struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"not null;size:128;uniqueIndex:uix_installed_component,priority:1;index:idx_installed_component_version,priority:1"`

	DeviceToken       string `gorm:"not null;size:128;uniqueIndex:uix_installed_component,priority:2;index"`
	DeviceIncarnation int64  `gorm:"not null;type:bigint;uniqueIndex:uix_installed_component,priority:3"`
	Component         string `gorm:"not null;size:64;uniqueIndex:uix_installed_component,priority:4;index:idx_installed_component_version,priority:2"`

	Version    string    `gorm:"not null;size:128;index:idx_installed_component_version,priority:3"`
	Digest     string    `gorm:"not null;size:71;default:''"`
	ObservedAt time.Time `gorm:"not null"`
	// Source is INVENTORY (the device said what it runs) or UPDATED (an update attempt reached UPDATED).
	Source string `gorm:"not null;size:16;check:chk_installed_component_source,source IN ('INVENTORY','UPDATED')"`
}

func (deviceInstalledComponent) TableName() string { return "device_installed_components" }

// NewInstalledComponentsSchema appends the installed-version store. A new table that no previous-release
// pod reads or writes, so old and new pods overlap safely; every statement is replayable (AutoMigrate
// carries the CHECK inside the CREATE TABLE).
func NewInstalledComponentsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: InstalledComponentsID,
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&deviceInstalledComponent{})
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
