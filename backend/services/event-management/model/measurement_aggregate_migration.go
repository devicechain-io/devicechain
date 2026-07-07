// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// rollupRefreshWindowDays is how far back the continuous-aggregate refresh policy
// re-processes invalidations each run. It bounds the trailing window kept in sync
// with late/out-of-order writes: telemetry backfilled with an occurred_time older
// than this window is NOT reflected in the rollup until a manual full refresh, so
// this must comfortably exceed how long a device can be offline and later flush its
// buffer. The invalidation log means an untouched window is near-free to re-scan, so
// this can be generous. A read that must be exact regardless (e.g. deep-historical
// backfill) can always take the raw path via the DisableRollupReads kill-switch.
const rollupRefreshWindowDays = 30

// NewMeasurementRollupAggregate realizes the ADR-026 continuous-aggregate rollup:
// a TimescaleDB continuous aggregate over measurement_events that pre-buckets
// telemetry into fixed base-bucket partial aggregates per (tenant, device, event
// type, measurement name), so BucketedMeasurements reads pre-computed rollups
// instead of scanning raw rows. The base bucket width is rollupBucketSeconds (the
// single source of truth shared with the read-path routing, so the two cannot
// drift).
//
// It stores sum/min/max/count — NOT avg — because only those roll up correctly to a
// coarser interval (avg is derived as sum/count at read time; see MeasurementRollup).
// Real-time aggregation is enabled (materialized_only = false) so a dashboard sees
// the still-un-materialized leading edge unioned live from the raw hypertable, not a
// gap up to the refresh lag. A full initial refresh materializes existing history;
// the refresh policy then keeps the trailing rollupRefreshWindowDays window current.
//
// The DDL is non-transactional (create-continuous-aggregate, refresh, and
// add_continuous_aggregate_policy each cannot run inside a transaction); the rdb
// migrator runs with UseTransaction=false, so each statement auto-commits — the same
// mechanism the create_hypertable calls in NewInitialSchema rely on. Because there is
// no wrapping transaction, a leading DROP ... IF EXISTS makes a re-run after a partial
// failure self-healing (gormigrate only records this migration's id on full success,
// so the DROP never runs against an already-committed production aggregate).
func NewMeasurementRollupAggregate() *gormigrate.Migration {
	const view = `"event-management"."measurement_rollups"`
	bucket := fmt.Sprintf("INTERVAL '%d seconds'", rollupBucketSeconds)
	return &gormigrate.Migration{
		ID: "20260707120000",
		Migrate: func(tx *gorm.DB) error {
			// Self-heal a partial prior attempt (see the doc comment): drop any orphaned
			// aggregate before recreating. A no-op on a clean install.
			if err := tx.Exec(`DROP MATERIALIZED VIEW IF EXISTS ` + view + `;`).Error; err != nil {
				return err
			}

			// Create the continuous aggregate WITH NO DATA (the portable form — WITH
			// DATA is rejected for continuous aggregates); the explicit full refresh
			// below materializes existing history. avg is intentionally absent (it does
			// not roll up); the read derives it from sum/count.
			if err := tx.Exec(`CREATE MATERIALIZED VIEW ` + view + `
				WITH (timescaledb.continuous) AS
				SELECT
					tenant_id,
					device_token,
					event_type,
					name,
					time_bucket(` + bucket + `, occurred_time) AS bucket,
					sum(value)   AS sum_value,
					min(value)   AS min_value,
					max(value)   AS max_value,
					count(value) AS count_value
				FROM "event-management"."measurement_events"
				GROUP BY tenant_id, device_token, event_type, name, bucket
				WITH NO DATA;`).Error; err != nil {
				return err
			}

			// Real-time aggregation: union the un-materialized leading edge from the raw
			// hypertable at query time so dashboards are not blind up to the refresh lag.
			if err := tx.Exec(`ALTER MATERIALIZED VIEW ` + view +
				` SET (timescaledb.materialized_only = false);`).Error; err != nil {
				return err
			}

			// Materialize all existing history once (NULL, NULL = the entire range). The
			// policy below only maintains a trailing window, so without this, data older
			// than that window would never be materialized. NOTE: on a very large existing
			// measurement_events this scans every chunk at deploy time and can be a long
			// migration step — acceptable pre-GA (small data); a huge backfill should be
			// materialized out-of-band in date-range chunks instead.
			if err := tx.Exec(`CALL refresh_continuous_aggregate(` +
				`'event-management.measurement_rollups', NULL, NULL);`).Error; err != nil {
				return err
			}

			// Refresh the trailing window every minute, leaving the current (still
			// filling) bucket to real-time aggregation. Immutable older history is
			// materialized once above and needs no re-refresh; late writes newer than the
			// window are picked up here (see rollupRefreshWindowDays).
			return tx.Exec(fmt.Sprintf(`SELECT add_continuous_aggregate_policy(`+
				`'event-management.measurement_rollups',`+
				` start_offset => INTERVAL '%d days',`+
				` end_offset => %s,`+
				` schedule_interval => INTERVAL '1 minute',`+
				` if_not_exists => true);`, rollupRefreshWindowDays, bucket)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP MATERIALIZED VIEW IF EXISTS ` + view + `;`).Error
		},
	}
}
