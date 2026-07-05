// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewFacetIndexSchema indexes the DeviceType identity facets manufacturer + model
// (ADR-045 decision 8). They back the distinct-value suggestion lists and are meant
// to be filtered/grouped on, so they get an index like DeviceProfile.category
// already has. Additive: AutoMigrate on a partial snapshot creates the missing
// indexes without touching columns.
func NewFacetIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705180000",
		Migrate: func(tx *gorm.DB) error {
			type DeviceType struct {
				gorm.Model
				Manufacturer sql.NullString `gorm:"size:128;index"`
				ModelName    sql.NullString `gorm:"column:model;size:128;index"`
			}
			return tx.AutoMigrate(&DeviceType{})
		},
		Rollback: func(tx *gorm.DB) error {
			// Drop by Go FIELD name (not column): gorm's Migrator resolves an index
			// through the struct's index tags to the same generated name AutoMigrate
			// created, whereas a bare column string never matches.
			type DeviceType struct {
				gorm.Model
				Manufacturer sql.NullString `gorm:"size:128;index"`
				ModelName    sql.NullString `gorm:"column:model;size:128;index"`
			}
			for _, field := range []string{"Manufacturer", "ModelName"} {
				if tx.Migrator().HasIndex(&DeviceType{}, field) {
					if err := tx.Migrator().DropIndex(&DeviceType{}, field); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}
