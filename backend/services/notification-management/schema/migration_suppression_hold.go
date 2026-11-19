// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewSuppressionHoldSchema appends nullable audit history, without a default or backfill.
// Literal, re-runnable DDL freezes the migration independently of the live model.
func NewSuppressionHoldSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261118000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "notification-management".notification_deferrals ADD COLUMN IF NOT EXISTS held_by_window varchar(128);`).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
