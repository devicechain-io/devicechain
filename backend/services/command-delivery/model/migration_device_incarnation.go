// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

type commandIncarnationSnapshot struct {
	DeviceIncarnation sql.NullInt64 `gorm:"type:bigint"`
}

type gateIncarnationSnapshot struct {
	DeletedIncarnation sql.NullInt64 `gorm:"type:bigint"`
}

// NewDeviceIncarnationSchema adds nullable identities so old pods can still insert rows.
func NewDeviceIncarnationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261118120000",
		Migrate: func(tx *gorm.DB) error {
			commands := tx.Table(tx.NamingStrategy.TableName("commands"))
			if !commands.Migrator().HasColumn(&commandIncarnationSnapshot{}, "DeviceIncarnation") {
				if err := commands.Migrator().AddColumn(&commandIncarnationSnapshot{}, "DeviceIncarnation"); err != nil {
					return err
				}
			}
			gates := tx.Table(tx.NamingStrategy.TableName("device_gates"))
			if !gates.Migrator().HasColumn(&gateIncarnationSnapshot{}, "DeletedIncarnation") {
				return gates.Migrator().AddColumn(&gateIncarnationSnapshot{}, "DeletedIncarnation")
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
