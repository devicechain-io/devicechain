// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Creates the initial schema migration for this functional area. Unlike the
// event-management schema this is a plain relational table (one row per device)
// and is NOT a hypertable.
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220601000000",
		Migrate: func(tx *gorm.DB) error {
			// Live current-state projection for one device.
			type DeviceState struct {
				gorm.Model
				rdb.TenantScoped
				DeviceId            uint `gorm:"uniqueIndex;not null"`
				Active              bool `gorm:"not null;default:false"`
				LastConnectTime     sql.NullTime
				LastDisconnectTime  sql.NullTime
				LastActivityTime    sql.NullTime
				InactivityAlarmTime sql.NullTime
				InactivityTimeout   int `gorm:"not null;default:600"`
			}

			return tx.AutoMigrate(&DeviceState{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("device_states")
		},
	}
}
