// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewMeasurementAggregationIndex backs the server-side aggregation read
// (BucketedMeasurements): it filters by tenant_id (+ usually device_token and a
// measurement name) over a time range and groups by time_bucket + name. The
// base (tenant_id, occurred_time DESC) index cannot serve the device/name
// filters, so a per-device time-series chart would scan the whole tenant's
// measurements. This composite index makes the common per-device (optionally
// per-metric) bucketed query index-only over its slice. The device is keyed by
// its token (ADR-044); device_token already carries C collation (initialschema),
// which this index inherits.
func NewMeasurementAggregationIndex() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260701120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_measurement_tenant_device_name_time ` +
				`ON "event-management"."measurement_events" (tenant_id, device_token, name, occurred_time DESC);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "event-management"."idx_measurement_tenant_device_name_time";`).Error
		},
	}
}
