// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantGeoFenceCapsSnapshot is this migration's own snapshot of iam_tenants: the primary key,
// and the three columns it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go. Pointing this at
// iam.Tenant would make this migration mean whatever that struct means on the day it runs —
// which breaks FRESH installs the moment the model gains another field (the baseline creates
// the table without it, this migration adds its columns, and the next fresh install replays
// into "column already exists") while every existing database applies cleanly and looks healthy.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type name and
// this migration silently creates a `tenant_geo_fence_caps_snapshots` table instead of altering
// the tenant row — succeeding, migrating nothing, and leaving every writer above it assigning to
// columns that do not exist. TestTheGeoFenceCapsMigrationDoesNotCreateAStrayTable is the guard.
type tenantGeoFenceCapsSnapshot struct {
	ID uint `gorm:"primarykey"`

	// All three NULLABLE, which is what makes "inherit the level below" expressible: null
	// means the tenant's tier decides, and failing that the platform default. A NOT NULL
	// column with a zero default would turn every un-configured tenant into one that has
	// explicitly chosen to hold NO geofence at all — every fence create refused — which is the
	// opposite of the inherit these columns exist to express. They are equally not
	// "unlimited": no value at any level means that.
	GeoFencePositionCeiling *int
	GeoFenceCeiling         *int
	GeoFencePositionBudget  *int
}

func (tenantGeoFenceCapsSnapshot) TableName() string { return "iam_tenants" }

// NewTenantGeoFenceCapsMigration adds the three per-tenant geofence caps (ADR-023 governance,
// packaged per the ADR-065 tier), each bounding a cost the other two cannot express:
//
//   - geo_fence_position_ceiling — one fence's position count, which bounds an O(V²) compile;
//   - geo_fence_ceiling — how many fences the tenant holds, which bounds the fence-set
//     manifest that must fit one broker message;
//   - geo_fence_position_budget — the position count across the tenant's WHOLE current fence
//     set, which bounds what that set costs to hold in event-processing's geometry cache — a
//     process every tenant on the instance shares.
//
// Three columns rather than one row in a settings blob, for the same reason every other
// governance override is a column: the cascade reads them per tenant on a hot-ish path, and a
// blob is unvalidated at rest.
//
// AutoMigrate is individually re-runnable — it adds each column if it is absent and is a no-op
// if it is present — which the chain requires, since migrations run with UseTransaction:false
// and replay from the top after a failure.
func NewTenantGeoFenceCapsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260825120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantGeoFenceCapsSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{
				"geo_fence_position_ceiling", "geo_fence_ceiling", "geo_fence_position_budget",
			} {
				if err := tx.Migrator().DropColumn(&tenantGeoFenceCapsSnapshot{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
