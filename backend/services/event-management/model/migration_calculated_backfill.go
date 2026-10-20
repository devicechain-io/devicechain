// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// calculatedBackfillsDDL is this migration's own snapshot of the job table: the statements
// that create it and its indexes, written as literals. Nothing here reads the baseline, a
// live model or another migration, so the live CalculatedBackfill struct can change without
// rewriting what a fresh install builds; a later change to the table is a new migration.
//
// A plain table, not a hypertable: a few rows per tenant for the life of the job, kept after
// it ends as the only record of what a backfill did. Every statement is IF NOT EXISTS, since
// this area's migrations run with UseTransaction:false and replay from the top after a
// failure.
var calculatedBackfillsDDL = []string{
	`CREATE TABLE IF NOT EXISTS "event-management".calculated_backfills (
		id bigserial PRIMARY KEY,
		tenant_id varchar(128) NOT NULL,
		token text NOT NULL,
		profile_token text NOT NULL,
		profile_version_token text NOT NULL,
		profile_version bigint NOT NULL,
		metric_keys jsonb NOT NULL,
		plan jsonb NOT NULL,
		warnings jsonb NOT NULL DEFAULT '[]',
		device_filter jsonb,
		range_from timestamptz NOT NULL,
		range_to timestamptz NOT NULL,
		status text NOT NULL,
		claim_epoch bigint NOT NULL DEFAULT 0,
		phase text NOT NULL DEFAULT 'FILL',
		roster_after_id bigint NOT NULL DEFAULT 0,
		cursor_window timestamptz,
		refresh_cursor timestamptz,
		devices_total bigint,
		devices_done bigint NOT NULL DEFAULT 0,
		samples bigint NOT NULL DEFAULT 0,
		computed bigint NOT NULL DEFAULT 0,
		written bigint NOT NULL DEFAULT 0,
		already_present bigint NOT NULL DEFAULT 0,
		windows_compressed bigint NOT NULL DEFAULT 0,
		windows_beyond_retention bigint NOT NULL DEFAULT 0,
		windows_refreshed bigint NOT NULL DEFAULT 0,
		skipped jsonb NOT NULL DEFAULT '{}',
		failure text,
		created_by text,
		created_at timestamptz NOT NULL,
		updated_at timestamptz NOT NULL,
		started_at timestamptz,
		finished_at timestamptz,
		cancel_requested_at timestamptz,
		CONSTRAINT chk_calculated_backfills_status
			CHECK (status IN ('QUEUED','RUNNING','SUCCEEDED','CANCELLED','FAILED')),
		CONSTRAINT chk_calculated_backfills_phase CHECK (phase IN ('FILL','REFRESH'))
	)`,
	// A job's identity within its tenant.
	`CREATE UNIQUE INDEX IF NOT EXISTS uix_calculated_backfills_tenant_token
		ON "event-management".calculated_backfills (tenant_id, token)`,
	// One active job per tenant, stated by the index itself: there is no `active` column to
	// leave stale, so a job that reaches a terminal status frees the tenant in the same
	// UPDATE.
	`CREATE UNIQUE INDEX IF NOT EXISTS uix_calculated_backfills_tenant_active
		ON "event-management".calculated_backfills (tenant_id) WHERE status IN ('QUEUED','RUNNING')`,
	// The worker's runnable scan, oldest first.
	`CREATE INDEX IF NOT EXISTS idx_calculated_backfills_status_updated
		ON "event-management".calculated_backfills (status, updated_at)`,
	// A profile's job list, newest first.
	`CREATE INDEX IF NOT EXISTS idx_calculated_backfills_tenant_profile
		ON "event-management".calculated_backfills (tenant_id, profile_token, created_at DESC)`,
}

// NewCalculatedBackfillSchema adds the calculated-metric backfill job table (ADR-082, S8).
//
// It is inert until a job row exists, and nothing creates one yet: the worker that reads the
// table runs on every start and finds it empty. No seed rows, no data written.
func NewCalculatedBackfillSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261019000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range calculatedBackfillsDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("calculated_backfills DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS "event-management".calculated_backfills`).Error
		},
	}
}
