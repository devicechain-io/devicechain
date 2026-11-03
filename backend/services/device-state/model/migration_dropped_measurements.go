// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, for the reason stated in
// baseline.go: a migration that can reach a live model is a migration whose output
// changes when that model does.
import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// droppedMeasurement is the snapshot of the dropped-measurement record as this migration
// creates it — one row per (tenant, device, name, reason).
//
// 🔴 IT IS NOT THE LIVE MODEL, and it must never be replaced by one: it is a frozen picture of
// a point in time, and model.DroppedMeasurement is the current incarnation of the same datatype.
// When this table gains a column, the column arrives in a NEW migration appended after this
// one; this struct stays exactly as it is.
//
// 🔴 NO TableName METHOD, deliberately: core/rdb pins the gorm NamingStrategy's TablePrefix to
// the functional area, and that prefix applies only when gorm DERIVES a table name. Gorm
// derives "dropped_measurements" from this type name, so RENAMING THIS TYPE RETARGETS THE
// MIGRATION.
//
// No deleted_at: nothing soft-deletes a diagnostic row; eviction and a tenant purge both delete
// it for good. gorm.Model is not embedded and tenant_id is inlined, matching
// baseline_snapshot.go.
type droppedMeasurement struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId    string    `gorm:"not null;size:128"`
	DeviceToken string    `gorm:"not null;type:varchar(128)"`
	Name        string    `gorm:"not null;size:128"`
	Reason      string    `gorm:"not null;size:32"`
	LastValue   string    `gorm:"not null;type:varchar(64)"`
	Count       int64     `gorm:"not null;default:0"`
	FirstTime   time.Time `gorm:"not null"`
	LastTime    time.Time `gorm:"not null"`
}

// NewDroppedMeasurementsSchema creates the dropped_measurements table: the per-device record
// of measurement keys the platform discarded at resolution, projected by the StateProcessor
// from the resolved-event stream beside the latest readings it is the counterpart of.
//
// It is an APPENDED migration; the baseline is frozen. Individually re-runnable, which it has to
// be since migrations run with UseTransaction:false and replay from the top after a failure:
// AutoMigrate issues CREATE TABLE IF NOT EXISTS and adds only missing columns, and the unique
// index is created conditionally.
//
// The unique index is raw DDL for the reason the baseline writes its own, and tenant_id LEADS it
// because a device token is unique only per tenant. It is also the only index the table needs:
// every read and the bound's eviction scan are by (tenant, device).
func NewDroppedMeasurementsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261102120000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT type above, never the live model.
			if err := tx.AutoMigrate(&droppedMeasurement{}); err != nil {
				return err
			}
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_dropped_measurement_tenant_device_name_reason ` +
				`ON "device-state".dropped_measurements (tenant_id, device_token, name, reason);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("dropped_measurements")
		},
	}
}
