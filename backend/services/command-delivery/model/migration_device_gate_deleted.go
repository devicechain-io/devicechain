// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// deviceGateDeletedSnapshot owns this migration's column shape. No TableName method:
// the table is resolved explicitly through the area's naming strategy, preserving its prefix.
type deviceGateDeletedSnapshot struct {
	DeletedThrough sql.NullTime
}

// NewDeviceGateDeletedSchema adds a nullable deletion floor. Old pods leave it NULL.
func NewDeviceGateDeletedSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261117120000",
		Migrate: func(tx *gorm.DB) error {
			table := tx.Table(tx.NamingStrategy.TableName("device_gates"))
			if table.Migrator().HasColumn(&deviceGateDeletedSnapshot{}, "DeletedThrough") {
				return nil
			}
			return table.Migrator().AddColumn(&deviceGateDeletedSnapshot{}, "DeletedThrough")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
