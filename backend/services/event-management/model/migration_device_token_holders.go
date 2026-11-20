// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Literal snapshot owned by this migration: when a live device was last known to hold each
// device token (DeviceTokenHolder). Tenant column as the watermark table's. Re-runnable after a
// partial run with UseTransaction:false. A new table only, so pods that predate it never read or
// write it and a rolling upgrade is safe.
var deviceTokenHoldersDDL = []string{
	`CREATE TABLE IF NOT EXISTS "event-management".device_token_holders (
		tenant_id varchar(128) NOT NULL,
		device_token varchar(128) NOT NULL,
		held_since timestamptz NOT NULL,
		PRIMARY KEY (tenant_id, device_token)
	)`,
}

func NewDeviceTokenHoldersSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261120000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range deviceTokenHoldersDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("device token holders DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
	}
}
