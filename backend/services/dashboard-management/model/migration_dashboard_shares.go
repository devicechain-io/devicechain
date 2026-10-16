// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// dashboardSharesStatements is the table this migration adds, written out literally. Like
// the published-version migration beside it, the DDL IS the snapshot: a new table could be
// created from a snapshot struct, but gorm cannot declare the foreign key's ON DELETE
// CASCADE or the CHECK on a struct without a relationship field, and both are what this
// table is for.
//
//   - The primary key is everything the row says: a share either exists or it does not.
//   - dashboard_id cascades from dashboards, so a share never outlives its dashboard.
//   - anchor_kind admits the two scope anchor kinds; the service accepts only customer today.
//   - idx_dashboard_shares_anchor serves the reverse lookup a deleted customer's shares are
//     removed by. The forward one — which dashboards a scope may see — is the primary key's
//     (tenant_id, dashboard_id) prefix.
var dashboardSharesStatements = []string{
	`CREATE TABLE IF NOT EXISTS "dashboard-management".dashboard_shares (
		tenant_id varchar(128) NOT NULL,
		dashboard_id bigint NOT NULL
			REFERENCES "dashboard-management".dashboards (id) ON DELETE CASCADE,
		anchor_kind varchar(16) NOT NULL CHECK (anchor_kind IN ('customer', 'group')),
		anchor_token varchar(128) NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tenant_id, dashboard_id, anchor_kind, anchor_token)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_dashboard_shares_anchor ` +
		`ON "dashboard-management".dashboard_shares (tenant_id, anchor_kind, anchor_token);`,
}

// NewDashboardSharesSchema adds dashboard_shares: the anchors a dashboard is shared with, so
// a member whose access is limited to part of the tenant lists and opens only the dashboards
// shared with an anchor in its scope (ADR-081). The table starts empty — nothing is shared
// until an author shares it — and holds a tenant column, so the tenant purge sweeps it with
// no list to maintain.
//
// Re-runnable (IF NOT EXISTS), as migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewDashboardSharesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016000100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range dashboardSharesStatements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS "dashboard-management".dashboard_shares;`).Error
		},
	}
}
