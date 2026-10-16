// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewQuietHoursSchema adds quiet hours to a notification policy and the table that holds
// the notifications quiet hours deferred.
//
// This is an APPENDED migration; the baseline is frozen.
//
// # The DDL is literal, and that is the snapshot
//
// The three policy columns go onto a table that already exists, so a second Go shape for
// notification_policies would only be a way to make gorm guess. notification_deferrals is
// new, but it is written out too: its unique index leads with tenant_id, which a gorm
// struct tag on an inlined mixin cannot spell without the index name tracking a Go field
// name. Writing the statements is the point-in-time shape this migration owns.
//
// Every policy column is NULLABLE with no default, and NULL is the true state of every
// policy that predates the feature ("no quiet hours", "nothing bypasses"), so there is no
// backfill.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: every
// statement is IF NOT EXISTS.
//
// # Tenant purge
//
// notification_deferrals carries tenant_id, so the catalog-driven tenant purge classifies
// it directly with no exemption to register.
func NewQuietHoursSchema() *gormigrate.Migration {
	const policies = `"notification-management".notification_policies`
	const deferrals = `"notification-management".notification_deferrals`
	statements := []string{
		`ALTER TABLE ` + policies + ` ADD COLUMN IF NOT EXISTS quiet_hours_timezone varchar(64);`,
		`ALTER TABLE ` + policies + ` ADD COLUMN IF NOT EXISTS quiet_hours_slots jsonb;`,
		`ALTER TABLE ` + policies + ` ADD COLUMN IF NOT EXISTS quiet_hours_bypass_severity varchar(16);`,
		`CREATE TABLE IF NOT EXISTS ` + deferrals + ` (
			id bigserial PRIMARY KEY,
			created_at timestamp with time zone,
			updated_at timestamp with time zone,
			tenant_id varchar(128) NOT NULL,
			alarm_token varchar(128) NOT NULL,
			policy_id bigint NOT NULL,
			alarm_key varchar(256) NOT NULL,
			severity varchar(16),
			occurred_time timestamp with time zone,
			release_at timestamp with time zone NOT NULL,
			attempts bigint NOT NULL DEFAULT 0,
			event jsonb NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uix_notification_deferrals_tenant_alarm_policy ON ` +
			deferrals + ` (tenant_id, alarm_token, policy_id);`,
		`CREATE INDEX IF NOT EXISTS "idx_notification-management_notification_deferrals_tenant_id" ON ` +
			deferrals + ` (tenant_id);`,
		`CREATE INDEX IF NOT EXISTS "idx_notification-management_notification_deferrals_policy_id" ON ` +
			deferrals + ` (policy_id);`,
		`CREATE INDEX IF NOT EXISTS "idx_notification-management_notification_deferrals_release_at" ON ` +
			deferrals + ` (release_at);`,
	}
	return &gormigrate.Migration{
		ID: "20261016000100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Only the table is undone; the nullable policy columns are left in place (dropping a
		// column destroys configuration, and a rollback exists to undo a failed forward
		// migration rather than to erase what ran after it).
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS ` + deferrals + `;`).Error
		},
	}
}
