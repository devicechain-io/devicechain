// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceIncarnationSchema adds the nullable device id a command was validated for and the
// deleted device id a gate's floor covers. Old pods leave both NULL.
//
// The snapshots are declared inside the migration and named for the tables they alter, so the
// area's naming strategy derives commands and device_gates (with the area prefix) from the type
// names; no TableName method, which would bypass that prefix.
func NewDeviceIncarnationSchema() *gormigrate.Migration {
	type command struct {
		DeviceIncarnation sql.NullInt64 `gorm:"type:bigint"`
	}
	type deviceGate struct {
		DeletedIncarnation sql.NullInt64 `gorm:"type:bigint"`
	}
	return &gormigrate.Migration{
		ID: "20261118120000",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&command{}, "DeviceIncarnation") {
				if err := tx.Migrator().AddColumn(&command{}, "DeviceIncarnation"); err != nil {
					return err
				}
			}
			if !tx.Migrator().HasColumn(&deviceGate{}, "DeletedIncarnation") {
				return tx.Migrator().AddColumn(&deviceGate{}, "DeletedIncarnation")
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
