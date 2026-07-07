// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewMeasurementBindingColumns denormalizes the bound metric definition's unit and
// data type onto each measurement row (ADR-016). The classifier already links a
// measurement to its definition, but that definition lives in device-management, so
// resolving unit/type on read would be a cross-service hop (ADR-044); storing them
// alongside the classifier at write time makes the row self-describing and keeps a
// historical row's unit/type stable across a later profile republish. Both are
// nullable — an undeclared (unbound) measurement carries neither. ADR-026 native
// compression collapses these low-cardinality columns to near-zero on disk. unit is
// unbounded text to match the source MetricDefinition.unit (so a long unit can never
// fail the INSERT); data_type is a closed enum, so a tight bound is safe.
//
// ADD COLUMN on a hypertable with pre-existing compressed chunks requires
// TimescaleDB >= 2.11 (satisfied by the pinned image); on a fresh cluster it is a
// metadata-only change. The measurement_rollups continuous aggregate does not
// reference these columns, so neither the add nor the rollback is blocked by it.
func NewMeasurementBindingColumns() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260707140000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "event-management"."measurement_events" ` +
				`ADD COLUMN IF NOT EXISTS unit text, ` +
				`ADD COLUMN IF NOT EXISTS data_type varchar(32);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "event-management"."measurement_events" ` +
				`DROP COLUMN IF EXISTS unit, DROP COLUMN IF EXISTS data_type;`).Error
		},
	}
}
