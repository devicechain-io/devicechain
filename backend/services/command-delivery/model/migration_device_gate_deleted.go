// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceGateDeletedSchema adds a nullable deletion floor. Old pods leave it NULL.
//
// The snapshot is declared inside the migration and named for the table it alters, so the
// area's naming strategy derives device_gates (with its prefix) from the type name; no
// TableName method, which would bypass that prefix.
func NewDeviceGateDeletedSchema() *gormigrate.Migration {
	type deviceGate struct {
		DeletedThrough sql.NullTime
	}
	return &gormigrate.Migration{
		ID: "20261117120000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&deviceGate{}, "DeletedThrough") {
				return nil
			}
			return tx.Migrator().AddColumn(&deviceGate{}, "DeletedThrough")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
