// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewInitialSchema creates the initial schema migration for the dashboard
// functional area. Like device-state this is a plain relational table (one row
// per dashboard), NOT a hypertable.
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220700000000",
		Migrate: func(tx *gorm.DB) error {
			// Persisted dashboard definition.
			type Dashboard struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				Definition datatypes.JSON `gorm:"not null"`
			}

			return tx.AutoMigrate(&Dashboard{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("dashboards")
		},
	}
}
