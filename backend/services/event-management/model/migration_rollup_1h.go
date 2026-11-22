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

// NewRollup1hSchema creates the one-hour continuous aggregate over measurement_rollups, its
// refresh policy and its initial materialization.
//
// 🔴 RE-RUNNABLE, WITHOUT RECOMPUTING HISTORY ON A REPLAY. Migrations run with UseTransaction:false
// and replay from the top after a failure, and create_materialized_view and the refresh cannot
// run in a transaction. The aggregate is created only when absent, the ALTER and the policy
// are idempotent, and the full-history refresh runs only while the aggregate has no refresh
// policy: the policy is the last step, so "no policy" means the run that created the aggregate
// never finished, and a replay of a finished migration over a live store does not pay for the
// whole history again.
func NewRollup1hSchema() *gormigrate.Migration {
	const view = `"event-management"."measurement_rollups_1h"`
	return &gormigrate.Migration{
		ID: "20261122030000",
		Migrate: func(tx *gorm.DB) error {
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

			var policies int64
			if err := tx.Raw(`SELECT count(*) FROM timescaledb_information.jobs ` +
				`WHERE hypertable_schema = 'event-management' AND hypertable_name = 'measurement_rollups_1h' ` +
				`AND proc_name = 'policy_refresh_continuous_aggregate'`).
				Scan(&policies).Error; err != nil {
				return fmt.Errorf("look for the measurement_rollups_1h refresh policy: %w", err)
			}
			if policies == 0 {
				// Materialize all existing history once; the policy below maintains only a
				// trailing window. NULL, NULL is the entire range.
				if err := tx.Exec(`CALL refresh_continuous_aggregate(` +
					`'event-management.measurement_rollups_1h', NULL, NULL);`).Error; err != nil {
					return err
				}
			}
			// The window ends one bucket back so the hour still filling is left to real-time
			// aggregation; late writes inside the window are picked up on the next pass.
			return tx.Exec(fmt.Sprintf(`SELECT add_continuous_aggregate_policy(`+
				`'event-management.measurement_rollups_1h',`+
				` start_offset => INTERVAL '%d days',`+
				` end_offset => INTERVAL '%d seconds',`+
				` schedule_interval => INTERVAL '5 minutes',`+
				` if_not_exists => true);`, rollup1hWindowDays, rollup1hBucketSeconds)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP MATERIALIZED VIEW IF EXISTS ` + view + `;`).Error
		},
	}
}
