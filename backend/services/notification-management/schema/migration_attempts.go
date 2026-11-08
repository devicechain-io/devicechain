// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewNotificationAttemptsSchema adds the delivery ledger: one row per attempt to send an
// alarm's notification through a channel.
//
// This is an APPENDED migration; the baseline is frozen.
//
// # The DDL is literal, and that is the snapshot
//
// Every index leads with tenant_id, so no single-column tenant_id index is needed. A gorm
// tag cannot spell such a composite without the index name tracking a Go field name; the
// statements below are the point-in-time shape this migration owns, so the live model can
// change without rewriting what a fresh install builds.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: every
// statement is IF NOT EXISTS.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge erases it with the
// tenant and the erasure fence refuses a late write for a purged one, with no exemption to
// register. That matters here more than for most tables: recipients are personal data.
func NewNotificationAttemptsSchema() *gormigrate.Migration {
	const attempts = `"notification-management".notification_attempts`
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + attempts + ` (
			id bigserial PRIMARY KEY,
			created_at timestamp with time zone NOT NULL,
			tenant_id varchar(128) NOT NULL,
			alarm_token varchar(128) NOT NULL,
			trigger_kind varchar(32) NOT NULL,
			policy_token varchar(128),
			channel_token varchar(128) NOT NULL,
			channel_type varchar(32) NOT NULL,
			recipients jsonb,
			subject_hash varchar(64),
			status varchar(32) NOT NULL,
			attempt bigint NOT NULL DEFAULT 0,
			max_attempts bigint NOT NULL DEFAULT 0,
			latency_millis bigint,
			error_code varchar(32),
			error_message varchar(512)
		);`,
		`CREATE INDEX IF NOT EXISTS ix_notification_attempts_tenant_time ON ` +
			attempts + ` (tenant_id, created_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS ix_notification_attempts_tenant_alarm ON ` +
			attempts + ` (tenant_id, alarm_token, created_at DESC, id DESC);`,
		// Status sits before time because the channel-health reads filter on it: the last
		// SENT, the last failure, and the failures since.
		`CREATE INDEX IF NOT EXISTS ix_notification_attempts_tenant_channel ON ` +
			attempts + ` (tenant_id, channel_token, status, created_at DESC, id DESC);`,
	}
	return &gormigrate.Migration{
		ID: "20261108000100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS ` + attempts + `;`).Error
		},
	}
}
