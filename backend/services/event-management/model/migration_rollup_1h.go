// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// RollupAggregate1h is the second rollup level: a hierarchical continuous aggregate at one hour
// over the one-minute measurement_rollups, retained on the same window as it.
const RollupAggregate1h = "measurement_rollups_1h"

// This migration's own literal snapshot of the second rollup level. Nothing here reads the
// baseline, a live model or another migration, so none of them can rewrite what a fresh install
// builds; a later change to the shape is a new migration.
//
// The aggregate stores the same four partials as the level under it (sum, min, max, count) and
// never an average, because only those roll up: avg is derived as sum/count at read time, which
// is exact, where an average of averages would not be. Min, max, sum and count of one-minute
// partials are the partials of the hour that holds them, so reading this level and re-bucketing
// the one-minute level return the same numbers.
//
// The group key names the time_bucket expression itself rather than the alias `bucket`: the
// source relation already has a column of that name, and GROUP BY resolves a bare name to the
// input column before the output alias, which would group by the minute and aggregate nothing.
//
// Real-time (materialized_only = false) like the level under it, so the hour still filling and
// the hours the refresh policy has not reached yet read correctly from the one-minute level
// beneath, which in turn unions the raw rows above ITS watermark.
//
// The one-hour level is additive: a pod from before this release neither reads nor writes it, so
// the old and new pods of a rolling upgrade overlap safely.
const (
	rollup1hBucketSeconds = 3600
	rollup1hWindowDays    = 30
)

var rollup1hCreateStmt = fmt.Sprintf(`CREATE MATERIALIZED VIEW "event-management"."measurement_rollups_1h"
	WITH (timescaledb.continuous) AS
	SELECT
		tenant_id,
		device_token,
		event_type,
		name,
		time_bucket(INTERVAL '%[1]d seconds', bucket) AS bucket,
		sum(sum_value)   AS sum_value,
		min(min_value)   AS min_value,
		max(max_value)   AS max_value,
		sum(count_value)::bigint AS count_value
	FROM "event-management"."measurement_rollups"
	GROUP BY tenant_id, device_token, event_type, name, time_bucket(INTERVAL '%[1]d seconds', bucket)
	WITH NO DATA;`, rollup1hBucketSeconds)

// rollupLevelsDDL is this migration's own snapshot of the table that records, per rollup level,
// the instant from which the level is complete. It names no tenant and holds no tenant's figures.
const rollupLevelsDDL = `CREATE TABLE IF NOT EXISTS "event-management".rollup_levels (
	level varchar(32) PRIMARY KEY,
	complete_from timestamptz NOT NULL
)`

// rollup1hLevel names the hour level's row in rollup_levels.
const rollup1hLevel = "1h"

// rollup1hMarkerStmt records the hour the aggregate is complete from: the next hour boundary
// after the moment the migration runs, in UTC so a server zone with a half-hour offset cannot
// move it off the hour. DO NOTHING keeps the first marker on a replay.
const rollup1hMarkerStmt = `INSERT INTO "event-management".rollup_levels (level, complete_from)
	VALUES ('1h', (date_trunc('hour', now() AT TIME ZONE 'UTC') + INTERVAL '1 hour') AT TIME ZONE 'UTC')
	ON CONFLICT (level) DO NOTHING`

// rollup1hChunkInterval is the hour materialization's chunk width, set explicitly so a retention
// job drops its chunks on time.
const rollup1hChunkInterval = "7 days"

// NewRollup1hSchema creates the one-hour continuous aggregate over measurement_rollups, its
// refresh policy and the marker that says from when it is complete. It is DDL ONLY.
//
// 🔴 IT DOES NOT FILL THE AGGREGATE, AND THAT IS THE DESIGN. A startup migration has a bounded
// window (the startup probe), and a refresh over a store's whole history is not bounded by it:
// the baseline's own comment measures ~90 s per 300k rows, and the refresh errors (55P03) when
// it meets the policy job's. So the aggregate is created WITH NO DATA and the policy maintains
// it from the moment it exists. History from before then is never filled by this migration, and
// the read path knows it: rollup_levels records the hour the aggregate is complete from, and
// the hour level is read only at or after that hour. Anything older is read from the minute
// level, exactly as before the aggregate existed, so an unfilled history never reads as empty.
// A follow-up may fill history in the background and move the marker back.
//
// Every statement is re-runnable (this area's migrations run with UseTransaction:false and
// replay from the top): the table and the aggregate are created only when absent, the ALTER,
// the chunk interval and the policy are idempotent, and the marker keeps its first value.
func NewRollup1hSchema() *gormigrate.Migration {
	const view = `"event-management"."measurement_rollups_1h"`
	return &gormigrate.Migration{
		ID: "20261122030000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(rollupLevelsDDL).Error; err != nil {
				return fmt.Errorf("create rollup_levels: %w", err)
			}
			var existing int64
			if err := tx.Raw(`SELECT count(*) FROM timescaledb_information.continuous_aggregates ` +
				`WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups_1h'`).
				Scan(&existing).Error; err != nil {
				return fmt.Errorf("look for an existing measurement_rollups_1h aggregate: %w", err)
			}
			if existing == 0 {
				if err := tx.Exec(rollup1hCreateStmt).Error; err != nil {
					return fmt.Errorf("create measurement_rollups_1h: %w", err)
				}
			}
			if err := tx.Exec(`ALTER MATERIALIZED VIEW ` + view +
				` SET (timescaledb.materialized_only = false);`).Error; err != nil {
				return err
			}
			// The materialization's default chunk width is a multiple of the level beneath's,
			// which is itself ten raw chunks, so a retention window would drop its chunks
			// months late. Chunks of a week keep that to a week.
			if err := tx.Exec(`SELECT set_chunk_time_interval('event-management.measurement_rollups_1h', ` +
				`INTERVAL '` + rollup1hChunkInterval + `');`).Error; err != nil {
				return fmt.Errorf("set the measurement_rollups_1h chunk interval: %w", err)
			}
			// Every minute, like the level beneath: the window ends one bucket back so the hour
			// still filling is left to real-time aggregation, and an hour closed by a late
			// write is corrected within the minute.
			if err := tx.Exec(fmt.Sprintf(`SELECT add_continuous_aggregate_policy(`+
				`'event-management.measurement_rollups_1h',`+
				` start_offset => INTERVAL '%d days',`+
				` end_offset => INTERVAL '%d seconds',`+
				` schedule_interval => INTERVAL '1 minute',`+
				` if_not_exists => true);`, rollup1hWindowDays, rollup1hBucketSeconds)).Error; err != nil {
				return err
			}
			return tx.Exec(rollup1hMarkerStmt).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP MATERIALIZED VIEW IF EXISTS ` + view + `;`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP TABLE IF EXISTS "event-management".rollup_levels;`).Error
		},
	}
}
