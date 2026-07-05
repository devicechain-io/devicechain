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
			type DeviceType struct {
				gorm.Model
				Manufacturer sql.NullString
				ModelName    sql.NullString `gorm:"column:model"`
			}
			for _, col := range []string{"manufacturer", "model"} {
				if tx.Migrator().HasIndex(&DeviceType{}, col) {
					if err := tx.Migrator().DropIndex(&DeviceType{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}
