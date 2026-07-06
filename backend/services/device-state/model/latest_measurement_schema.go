// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewLatestMeasurementSchema creates the latest-measurement projection table: the
// current value of each named measurement per device (one row per tenant/device/
// name), maintained by the StateProcessor from the resolved-event stream. Like
// device_states it is a plain relational table, not a hypertable.
func NewLatestMeasurementSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260701130000",
		Migrate: func(tx *gorm.DB) error {
			type LatestMeasurement struct {
				gorm.Model
				rdb.TenantScoped
				DeviceToken  string          `gorm:"not null;type:varchar(128)"`
				Name         string          `gorm:"not null;size:128"`
				Value        sql.NullFloat64 `gorm:"type:decimal(20,8)"`
				Classifier   *uint
				OccurredTime time.Time `gorm:"not null"`
			}
			if err := tx.AutoMigrate(&LatestMeasurement{}); err != nil {
				return err
			}
			// One row per (tenant, device, measurement name). The unique index backs
			// the tenant-scoped per-device read and guards against duplicate creates
			// racing across pods (a losing concurrent create errors and is redelivered).
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_latest_measurement_tenant_device_name ` +
				`ON "device-state".latest_measurements (tenant_id, device_token, name);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("latest_measurements")
		},
	}
}
