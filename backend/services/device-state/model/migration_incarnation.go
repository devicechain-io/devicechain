// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceStateIncarnationSchema expands the table without changing old pods' writes.
func NewDeviceStateIncarnationSchema() *gormigrate.Migration {
	// This local snapshot's name derives device_states through the area's naming
	// strategy, including its schema prefix. It does not reuse the baseline snapshot.
	type deviceState struct {
		Incarnation sql.NullInt64 `gorm:"type:bigint"`
	}
	return &gormigrate.Migration{
		ID: "20261115120000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&deviceState{}, "Incarnation") {
				return nil
			}
			return tx.Migrator().AddColumn(&deviceState{}, "Incarnation")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
