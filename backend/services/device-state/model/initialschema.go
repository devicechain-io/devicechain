// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

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
			// Live current-state projection for one device. A device token is unique
			// only PER TENANT (ADR-042), so the uniqueness constraint must be the
			// composite (tenant_id, device_token) — a bare unique index on
			// device_token would reject a second tenant that reuses another tenant's
			// token, failing every MergeDeviceState for that device. tenant_id is
			// declared explicitly here (rather than via the rdb.TenantScoped embed the
			// runtime model still uses) so it can lead the composite unique index.
			type DeviceState struct {
				gorm.Model
				TenantId            string `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:1;not null;size:128"`
				DeviceToken         string `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:2;not null;type:varchar(128)"`
				Active              bool   `gorm:"not null;default:false"`
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
