// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantUsageIngestDDL is this migration's own snapshot: literal statements, reading no live model
// and no other migration.
//
// It moves the usage rollup onto INGEST time (events.processed_time, when the platform received
// the event) and gives it what that needs:
//
//   - A BRIN index on events.processed_time. The rollup reads one hour of ingest time at a time,
//     and processed_time is not the hypertable's partition column, so without an index every pass
//     reads every chunk. BRIN fits a column that rises with insertion order: it is a few pages per
//     chunk and is cheap to maintain on the write path. It does a second job on COMPRESSED chunks:
//     TimescaleDB records per-batch min/max for an indexed column when it compresses a chunk, so a
//     compressed chunk's batches are filtered on that metadata instead of being decompressed.
//     `timescaledb.transaction_per_chunk` builds it one chunk per transaction, so an upgrade on a
//     large store does not hold writes to the whole hypertable for the length of the build.
//   - tenant_usage_state: one row per rollup, holding the next hour it owes. It is read and
//     advanced under the rollup's lock, in the same transaction as each hour, so every replica
//     resumes from the same place and an hour is computed once.
//
// Every statement is re-runnable (IF NOT EXISTS): this area's migrations run with
// UseTransaction:false, so a half-applied run replays from the top. A per-chunk build that fails
// part-way leaves the index on the chunks it reached; a re-run skips the statement because the
// index exists, and the chunks it missed are read without it (slower, never wrong).
var tenantUsageIngestDDL = []string{
	`CREATE INDEX IF NOT EXISTS idx_events_processed_time_brin ON "event-management".events
		USING brin (processed_time) WITH (timescaledb.transaction_per_chunk)`,
	`CREATE TABLE IF NOT EXISTS "event-management".tenant_usage_state (
		name varchar(64) NOT NULL,
		next_hour timestamptz NOT NULL,
		scan_floor timestamptz,
		updated_at timestamptz NOT NULL,
		PRIMARY KEY (name)
	)`,
}

// NewTenantUsageIngestSchema adds the ingest-time index and the rollup's persisted watermark. It
// seeds nothing: the rollup writes its state row on its first pass.
func NewTenantUsageIngestSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261108000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range tenantUsageIngestDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("tenant usage ingest DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP TABLE IF EXISTS "event-management".tenant_usage_state`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP INDEX IF EXISTS "event-management".idx_events_processed_time_brin`).Error
		},
	}
}
