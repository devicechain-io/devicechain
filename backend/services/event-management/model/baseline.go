// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary, a live validator or a live CONSTANT is a migration whose behaviour changes
// when those do.
import (
	"database/sql"
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Frozen literals for the continuous aggregate's shape.
//
// 🔴 THESE ARE COPIES, ON PURPOSE. The migration this baseline replaces built its DDL from
// rollupBucketSeconds in api_read.go — a constant that belongs to the READ path. That is the
// snapshot rule broken through a constant rather than a struct: retuning the read-side bucket
// would silently change what an already-applied migration creates, and the aggregate's stored
// bucket width would no longer be what any migration says it is. The read path and the
// aggregate genuinely have to agree, but the way to keep them agreeing is a test, not a shared
// symbol that lets one quietly redefine the other's history.
const (
	baselineRollupBucketSeconds = 60
	baselineRollupWindowDays    = 30
)

// baselineHypertables is every table converted by create_hypertable, in creation order.
//
// 🔴 HYPERTABLE-NESS IS THE PROPERTY THIS AREA'S FLATTEN IS MOST LIKELY TO LOSE, AND IT USED
// TO BE INVISIBLE. A hypertable's identity lives in _timescaledb_catalog, which a
// `pg_dump --schema event-management` never visits, so a plain table and a hypertable dumped
// identically apart from the `<table>_occurred_time_idx` index Timescale creates for you. A
// flatten written from the golden could therefore write that index by hand, omit
// create_hypertable, and pass. migrationdiff now probes the catalog (see
// backend/tools/migrationdiff/timescale.go), which is what makes this list checkable at all.
//
// What it costs to get wrong was measured on a live cluster during the ADR-028 event-store
// restore drill: losing Timescale's machinery left the cluster reporting healthy, the extension
// still listed, the API answering — and every row in a compressed chunk returning as nothing.
var baselineHypertables = []string{
	"events",
	"location_events",
	"measurement_events",
	"alert_events",
	"event_anchors",
	"state_change_events",
}

// baselineCollatedColumns is every opaque token column forced to C collation, per table.
//
// The tokens are only ever exact-matched, so C gives bytewise memcmp comparisons with no locale
// overhead and smaller, tighter B-trees on these high-volume hypertables (ADR-044). ORDER
// MATTERS RELATIVE TO THE INDEXES: an index inherits its column's collation, so every one of
// these runs BEFORE the indexes below.
var baselineCollatedColumns = []struct {
	table   string
	columns []string
}{
	{"events", []string{"device_token"}},
	{"location_events", []string{"device_token"}},
	{"measurement_events", []string{"device_token"}},
	{"alert_events", []string{"device_token"}},
	{"event_anchors", []string{"device_token", "anchor_token"}},
	{"state_change_events", []string{"device_token"}},
}

// NewBaselineSchema materializes the whole event-management schema in one step: the base event
// hypertable and its three payload hypertables, the event-anchor set (ADR-013 addendum), the
// presence-history timeline (ADR-067 S3), every index, and the measurement_rollups continuous
// aggregate with its refresh policy (ADR-026).
//
// This REPLACES the former seven-migration chain, collapsed pre-GA. Equivalence is proven by
// hack/migration-diff.sh verify against the golden captured from that chain — including, now,
// the Timescale catalog probe, so "the tables came back" is not mistaken for "the hypertables
// came back".
//
// Three of the seven fold in as ABSENCES or APPENDS rather than as DDL, and each is called out
// where it lands: the base event's dropped anchor_type/anchor_id pair and its dropped index
// (see the `event` snapshot type), and measurement_events' appended unit/data_type. None of the
// seven carried data — no seed, no backfill.
//
// THE DDL HERE IS NON-TRANSACTIONAL AND THAT IS LOAD-BEARING. create_hypertable,
// create-continuous-aggregate, refresh_continuous_aggregate and add_continuous_aggregate_policy
// each cannot run inside a transaction; core/rdb runs migrations with UseTransaction=false so
// every statement auto-commits. The consequence is that a half-applied run is NEVER rolled
// back and replays from the top on the next boot, which is why every statement below is
// individually re-runnable (IF NOT EXISTS, if_not_exists => TRUE, and a leading DROP for the
// aggregate). Anything appended after this migration must hold the same property.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this baseline,
// it is recreated (dcctl destroy + bootstrap).
func NewBaselineSchema() *gormigrate.Migration {
	const view = `"event-management"."measurement_rollups"`

	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models.
			if err := tx.AutoMigrate(
				&event{},
				&locationEvent{},
				&measurementEvent{},
				&alertEvent{},
				&eventAnchor{},
				&stateChangeEvent{},
			); err != nil {
				return err
			}

			// Convert to hypertables, all partitioned on occurred_time so the payload
			// chunks age out alongside the parent's (ADR-026 amd).
			for _, table := range baselineHypertables {
				if err := tx.Exec(fmt.Sprintf(
					`SELECT create_hypertable('event-management.%s', 'occurred_time', if_not_exists => TRUE);`,
					table)).Error; err != nil {
					return fmt.Errorf("create_hypertable %s: %w", table, err)
				}
			}

			// C collation BEFORE the indexes, which inherit it.
			//
			// 🔴 SKIPPED WHEN ALREADY C, AND THAT CONDITIONAL IS THE WHOLE REASON THIS
			// MIGRATION CAN BE RUN TWICE. Migrations run with UseTransaction:false and
			// replay from the top after a failure, so a baseline that fails partway is
			// run again over its own half-built schema. This step is where that used to
			// die: the continuous aggregate created further down SURVIVES the failed
			// run, and Postgres then refuses to re-type a column a view reads —
			// "cannot alter type of a column used by a view or rule" (SQLSTATE 0A000).
			// The operator saw event-management crash-looping on an error naming a
			// VIEW, with no mention of the migration that was wedged, on a schema too
			// half-built to serve, and no forward path.
			//
			// Re-typing a column that is already varchar(128) COLLATE "C" is a no-op
			// with a side effect — it rewrites the table and invalidates dependents —
			// so skipping it is not merely cheaper, it is the correct statement of what
			// this step means: "ensure C collation", not "convert now".
			//
			// The resulting schema is byte-identical either way, which is why this
			// re-cut needs no golden update and no existing instance has to be
			// recreated: an instance that already applied this id never runs it again,
			// and one that never applied it builds exactly what it built before.
			for _, t := range baselineCollatedColumns {
				for _, col := range t.columns {
					var collation sql.NullString
					if err := tx.Raw(
						`SELECT collation_name FROM information_schema.columns `+
							`WHERE table_schema = 'event-management' AND table_name = ? AND column_name = ?`,
						t.table, col).Scan(&collation).Error; err != nil {
						return fmt.Errorf("read collation of %s.%s: %w", t.table, col, err)
					}
					if collation.Valid && collation.String == "C" {
						continue
					}
					if err := tx.Exec(fmt.Sprintf(
						`ALTER TABLE "event-management".%q ALTER COLUMN %s TYPE varchar(128) COLLATE "C";`,
						t.table, col)).Error; err != nil {
						return fmt.Errorf("collate %s.%s: %w", t.table, col, err)
					}
				}
			}

			// 🔴 THESE NAMES ARE POSTGRES'S OWN DERIVATION, SPELLED OUT, AND BOTH HALVES
			// OF THAT MATTER.
			//
			// They used to be written unnamed, deliberately, so Postgres would derive
			// `<table>_<columns>_idx` — the name the pre-baseline chain produced and
			// therefore the name the golden holds. Naming them differently would have
			// been a schema difference dressed as a tidy-up.
			//
			// But an unnamed CREATE INDEX is not re-runnable, and it does not announce
			// that: run twice, Postgres does NOT collide, it silently creates
			// `<name>1` — a duplicate index written on every insert and read by nothing.
			// Replay is a real path (UseTransaction:false, replay from the top after a
			// failure), so the unnamed form was a silent cost on exactly the runs that
			// were already going badly.
			//
			// Writing the derived name explicitly with IF NOT EXISTS keeps the schema
			// byte-identical to the golden AND makes the step re-runnable. The names
			// below are transcribed from golden/event-management.sql, not guessed; if
			// you change a column list you must change the name to match what Postgres
			// would derive, and `hack/migration-diff.sh verify` is what catches you.
			for _, stmt := range []string{
				`CREATE INDEX IF NOT EXISTS events_device_token_occurred_time_idx ` +
					`ON "event-management"."events" (device_token, occurred_time DESC);`,
				`CREATE INDEX IF NOT EXISTS events_tenant_id_occurred_time_idx ` +
					`ON "event-management"."events" (tenant_id, occurred_time DESC);`,
				`CREATE INDEX IF NOT EXISTS location_events_tenant_id_occurred_time_idx ` +
					`ON "event-management"."location_events" (tenant_id, occurred_time DESC);`,
				`CREATE INDEX IF NOT EXISTS measurement_events_tenant_id_occurred_time_idx ` +
					`ON "event-management"."measurement_events" (tenant_id, occurred_time DESC);`,
				`CREATE INDEX IF NOT EXISTS alert_events_tenant_id_occurred_time_idx ` +
					`ON "event-management"."alert_events" (tenant_id, occurred_time DESC);`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}

			// The named indexes.
			for _, stmt := range []string{
				// Idempotent ingestion on the alternateId. Redelivery is a designed-for path
				// (ADR-022), so without this a redelivered resolved event double-persists.
				// Timescale requires every unique index on a hypertable to include the
				// partitioning column, so the key is (tenant_id, alt_id, occurred_time) — a
				// redelivery carries the identical occurred_time and still collides. Partial
				// on alt_id so events without one are simply not deduplicated.
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_tenant_alt_id ` +
					`ON "event-management"."events" (tenant_id, alt_id, occurred_time) WHERE alt_id IS NOT NULL;`,
				// The natural key as a QUERY path. It used to be the primary key, which gave
				// this index for free — until keying on it turned out to drop one of any two
				// distinct events that shared it. The reads that filtered on it are still
				// there ("this device's alerts over this window"), so the access path is kept
				// deliberately while the uniqueness claim is gone. NOT UNIQUE, and it must
				// never become unique again.
				`CREATE INDEX IF NOT EXISTS idx_events_tenant_device_type_time ` +
					`ON "event-management"."events" (tenant_id, device_token, event_type, occurred_time DESC);`,
				// Anchors are read by their event's identity (AnchorsForEvent), and the same
				// index makes the anchor set idempotent: one event is anchored to a given
				// target at most once, so these columns ARE the identity. UNIQUE, because a
				// redelivery re-presents the whole set and nothing else stops it — the
				// Deduped skip in persistEventAnchors only ever fires for state changes.
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_event_anchors_idem ` +
					`ON "event-management"."event_anchors" (tenant_id, event_id, occurred_time, anchor_type, anchor_token);`,
				// Per-row idempotency for the payload tables. state_change_events already had
				// exactly this (uq_state_change_events_idem) and for exactly this reason — a
				// row whose event carries no alternateId has nothing else to dedup it. These
				// three tables were left out; a redelivery duplicated every one of their rows.
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_location_events_idem ` +
					`ON "event-management"."location_events" (tenant_id, payload_id, occurred_time);`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_measurement_events_idem ` +
					`ON "event-management"."measurement_events" (tenant_id, payload_id, occurred_time);`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_events_idem ` +
					`ON "event-management"."alert_events" (tenant_id, payload_id, occurred_time);`,
				// Backs the server-side bucketed read: the base (tenant_id, occurred_time)
				// index cannot serve the device/name filters, so a per-device chart would
				// otherwise scan the whole tenant's measurements.
				`CREATE INDEX IF NOT EXISTS idx_measurement_tenant_device_name_time ` +
					`ON "event-management"."measurement_events" (tenant_id, device_token, name, occurred_time DESC);`,
				// "events for customer X / area Y, most recent first".
				`CREATE INDEX IF NOT EXISTS idx_event_anchors_lookup ` +
					`ON "event-management"."event_anchors" (tenant_id, anchor_type, anchor_token, occurred_time DESC);`,
				// A device's presence timeline.
				`CREATE INDEX IF NOT EXISTS idx_state_change_events_lookup ` +
					`ON "event-management"."state_change_events" (tenant_id, device_token, occurred_time DESC);`,
				// Presence-row redelivery dedup. A birth+death at one instant differ by state
				// and both survive; a late higher-session echo differs by session_id and is
				// kept for audit; a true redelivery collides and is dropped.
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_state_change_events_idem ` +
					`ON "event-management"."state_change_events" (tenant_id, device_token, occurred_time, state, session_id);`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}

			return createMeasurementRollups(tx, view)
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP MATERIALIZED VIEW IF EXISTS ` + view + `;`).Error; err != nil {
				return err
			}
			for _, table := range []string{
				"state_change_events",
				"event_anchors",
				"alert_events",
				"measurement_events",
				"location_events",
				"events",
			} {
				if err := tx.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS "event-management".%q;`, table)).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// createMeasurementRollups builds the measurement_rollups continuous aggregate (ADR-026).
//
// It stores sum/min/max/count and NOT avg, because only those roll up correctly to a coarser
// interval; avg is derived as sum/count at read time.
//
// 🔴 IT CREATES THE AGGREGATE ONLY IF IT IS ABSENT, AND THAT REPLACED A LEADING DROP.
//
// The DROP was there to make a re-run after a partial failure self-healing, on the reasoning that
// gormigrate records this migration's id only on full success, so it could never run against an
// already-committed production aggregate. That reasoning is correct and it was still the wrong
// mechanism: a replay then DROPPED and RECREATED the aggregate, which discards its materialization
// and re-runs a full refresh. The replay gate sees it as what it is — the migration ran twice and
// changed a Timescale object, `_materialized_hypertable_7` becoming `_8`.
//
// Skipping when present is self-healing in the same way and identical on replay. The three steps
// after the create are already idempotent by construction: SET materialized_only is a plain ALTER,
// refresh_continuous_aggregate recomputes, and the policy carries if_not_exists. So a partial run
// that got as far as the view still has each of them applied on the way through.
//
// The shape is not re-checked, and that is deliberate rather than an omission: an aggregate at this
// name can only have been created by this function, in this frozen migration, so "it exists" and
// "it has the right shape" are the same statement. A later change to the shape is a NEW migration
// with its own id, which is where that question belongs.
func createMeasurementRollups(tx *gorm.DB, view string) error {
	bucket := fmt.Sprintf("INTERVAL '%d seconds'", baselineRollupBucketSeconds)

	var existing int64
	if err := tx.Raw(
		`SELECT count(*) FROM timescaledb_information.continuous_aggregates ` +
			`WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups'`).
		Scan(&existing).Error; err != nil {
		return fmt.Errorf("look for an existing measurement_rollups aggregate: %w", err)
	}
	if existing > 0 {
		return ensureRollupSettings(tx, view, bucket)
	}

	// WITH NO DATA is the portable form — WITH DATA is rejected for continuous aggregates —
	// so the explicit full refresh below is what materializes existing history.
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

	return ensureRollupSettings(tx, view, bucket)
}

// ensureRollupSettings applies the three idempotent steps that follow the aggregate's creation.
//
// They are split out so the create-if-absent path above and the fresh path run exactly the same
// statements. Inlining them twice is how the two paths drift, and a replay that skipped one of
// these would leave an aggregate that reads correctly and never materializes.
func ensureRollupSettings(tx *gorm.DB, view, bucket string) error {
	// Real-time aggregation: union the un-materialized leading edge from the raw hypertable at
	// query time, so a dashboard is not blind up to the refresh lag.
	//
	// 🔴 This is also why a rollup check must read the MATERIALIZATION hypertable rather than
	// the view. Measured during the ADR-028 drill: with materialized_only = false the view
	// recomputes anything ABOVE the aggregate's watermark live, so it returns correct numbers
	// for that range whether or not those rows were ever materialized — and the watermark is
	// itself restored state, so a view read answers about the watermark and the data at once.
	if err := tx.Exec(`ALTER MATERIALIZED VIEW ` + view +
		` SET (timescaledb.materialized_only = false);`).Error; err != nil {
		return err
	}

	// Materialize all existing history once (NULL, NULL = the entire range). The policy below
	// only maintains a trailing window, so without this, data older than that window would
	// never be materialized at all.
	if err := tx.Exec(`CALL refresh_continuous_aggregate(` +
		`'event-management.measurement_rollups', NULL, NULL);`).Error; err != nil {
		return err
	}

	// Refresh the trailing window every minute, leaving the current (still filling) bucket to
	// real-time aggregation. Immutable older history is materialized once above; late writes
	// newer than the window are picked up here.
	return tx.Exec(fmt.Sprintf(`SELECT add_continuous_aggregate_policy(`+
		`'event-management.measurement_rollups',`+
		` start_offset => INTERVAL '%d days',`+
		` end_offset => %s,`+
		` schedule_interval => INTERVAL '1 minute',`+
		` if_not_exists => true);`, baselineRollupWindowDays, bucket)).Error
}
