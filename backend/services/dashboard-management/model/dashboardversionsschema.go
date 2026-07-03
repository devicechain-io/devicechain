// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewDashboardVersionsSchema adds the dashboard_versions table (ADR-039 versioning):
// immutable published snapshots of a dashboard's definition, addressed by parent +
// monotonic version. The struct is re-declared locally (the gormigrate convention)
// so the migration is pinned to the shape at this point in history, independent of
// later model changes. No token index (a version has no token, unlike Dashboard);
// the (dashboard_id, version) unique index comes from the struct tags via AutoMigrate.
func NewDashboardVersionsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260703000000",
		Migrate: func(tx *gorm.DB) error {
			type DashboardVersion struct {
				gorm.Model
				rdb.TenantScoped
				DashboardID uint           `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:1"`
				Version     int32          `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:2"`
				Label       sql.NullString `gorm:"size:128"`
				Description sql.NullString `gorm:"size:1024"`
				Definition  datatypes.JSON `gorm:"not null"`
				PublishedBy string         `gorm:"size:256"`
			}
			return tx.AutoMigrate(&DashboardVersion{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("dashboard_versions")
		},
	}
}
