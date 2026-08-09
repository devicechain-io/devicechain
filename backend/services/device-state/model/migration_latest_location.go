// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, for the reason stated in
// baseline.go: a migration that can reach a live model is a migration whose output
// changes when that model does.
import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// latestLocation is the snapshot of the last-known-position projection as this
// migration creates it — one row per (tenant, device).
//
// 🔴 IT IS NOT THE LIVE MODEL, and it must never be replaced by one. It is a frozen
// picture of a point in time; model.LatestLocation is the current incarnation of the
// same datatype. If this pointed at the live model, then the day someone widens
// LatestLocation.Heading every EXISTING database would keep the old column while every
// FRESH install silently got the new one — and the install that looks healthy is the
// one that is wrong. When this table gains a column, the column arrives in a NEW
// migration appended after this one; this struct stays exactly as it is.
//
// 🔴 NO TableName METHOD, deliberately. core/rdb pins the gorm NamingStrategy's
// TablePrefix to the functional area, and that prefix applies only when gorm DERIVES a
// table name; an explicit TableName bypasses it and every index on the table is
// silently renamed (idx_device-state_latest_locations_deleted_at →
// idx_latest_locations_deleted_at), which the golden schema diff catches. Gorm derives
// "latest_locations" from this type name, so RENAMING THIS TYPE RETARGETS THE
// MIGRATION. Leave both the absence and the name alone.
//
// gorm.Model and rdb.TenantScoped are INLINED rather than embedded, matching
// baseline_snapshot.go: nothing here is reachable from backend/core, so a change to a
// core mixin cannot rewrite this migration from a PR that never touched this service.
//
// The column widths are literals copied from event-management's LocationEvent rather
// than derived from anything, and they are the same widths on purpose: this projection
// and that history are two views of one fix, so they must store it at one precision.
// Latitude/longitude are degrees — 8 fractional digits (~1.1mm) with just enough integer
// room for ±90 / ±180. Elevation, accuracy and speed are metres (or metres per second),
// which need integer range rather than sub-degree precision. Heading is a compass
// bearing bounded to [0, 360) by its own semantics.
type latestLocation struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceToken  string          `gorm:"not null;type:varchar(128)"`
	Latitude     sql.NullFloat64 `gorm:"type:decimal(10,8)"`
	Longitude    sql.NullFloat64 `gorm:"type:decimal(11,8)"`
	Elevation    sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Accuracy     sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Speed        sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Heading      sql.NullFloat64 `gorm:"type:decimal(7,4)"`
	OccurredTime time.Time       `gorm:"not null"`
}

// NewLatestLocationSchema creates the latest_locations table: the O(1) "where is this
// device now?" projection maintained by the StateProcessor from the resolved-event
// stream, beside the append-only location history in event-management.
//
// This is an APPENDED migration. The baseline (NewBaselineSchema) is frozen and is not
// edited to add this table — a fresh install creates the baseline's two tables and then
// creates this one, landing it in exactly the position an upgraded database has it.
//
// It is individually re-runnable, which it has to be: migrations run with
// UseTransaction:false, so a half-applied migration is never rolled back and replays
// from the top on the next boot. Both statements are therefore IF NOT EXISTS —
// AutoMigrate issues CREATE TABLE IF NOT EXISTS and adds only missing columns, and the
// index is created conditionally.
//
// The unique index is raw DDL rather than a struct tag for the same reason the baseline
// writes its own: the index is created against the area-qualified table, and an index
// name is schema — core must not be able to change this migration's output by changing a
// helper. tenant_id LEADS the index because a device token is unique only per tenant
// (ADR-042); a bare unique index on device_token would reject a second tenant reusing
// another tenant's token and fail every location merge for that device.
func NewLatestLocationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260809120000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT type above, never the live model.
			if err := tx.AutoMigrate(&latestLocation{}); err != nil {
				return err
			}
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_latest_location_tenant_device ` +
				`ON "device-state".latest_locations (tenant_id, device_token);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("latest_locations")
		},
	}
}
