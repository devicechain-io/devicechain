// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewLatestMeasurementBindingColumns denormalizes the bound metric definition's
// unit + data type onto the latest-measurement projection (ADR-016), mirroring the
// event-management measurement_events history projection so the last-known value is
// self-describing without a cross-service hop into device-management. Both are
// nullable — an undeclared (unbound) measurement carries neither. unit is unbounded
// text to match the source column; data_type is a closed enum.
func NewLatestMeasurementBindingColumns() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260707150000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".latest_measurements ` +
				`ADD COLUMN IF NOT EXISTS unit text, ` +
				`ADD COLUMN IF NOT EXISTS data_type varchar(32);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "device-state".latest_measurements ` +
				`DROP COLUMN IF EXISTS unit, DROP COLUMN IF EXISTS data_type;`).Error
		},
	}
}
