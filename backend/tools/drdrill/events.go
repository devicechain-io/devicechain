// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// areaEvent is the event-management functional area — the `/api/<area>` ingress
// prefix and the Postgres schema its tables live in, which the chart derives from
// the same key. One constant for both, so the drill cannot look for the rows in
// one place and query the API in another.
const areaEvent = "event-management"

// rollupView is the continuous aggregate under test, as the migration names it.
const rollupView = "measurement_rollups"

// The hypertables event-management persists into. Listed here rather than
// discovered, because the point of the check is that the restore brought back the
// set the platform expects — a discovered list would happily report that all zero
// of the hypertables it found are healthy.
//
// 🔴 SIX, not the four in the initial schema. event_anchors and
// state_change_events were each converted by a LATER migration, and a list
// written from NewInitialSchema alone silently under-checks by a third. That is
// why assertEventSchema cross-checks this against the count in the database
// rather than only looking up the names it knows.
var eventHypertables = []string{
	"events", "location_events", "measurement_events", "alert_events",
	"event_anchors", "state_change_events",
}

// EventPasswordEnv is the environment variable the event half reads its Postgres
// password from when --db-password is not given.
//
// 🔴 It is NOT PGPASSWORD, which the secret half falls back to, and the
// difference is load-bearing rather than tidiness. An instance runs TWO Postgres
// servers with two different application passwords. A rig that exported one
// PGPASSWORD would hand the relational store's password to the event store, and
// the failure — "connect: password authentication failed" — reads as an event
// store that did not come back. Two names cannot collide.
const EventPasswordEnv = "DRDRILL_EVENT_PGPASSWORD"

func eventPassword() string { return os.Getenv(EventPasswordEnv) }

// openPostgres opens a single-connection pool against one database.
//
// It is shared by the secret half and the event half because both had the same
// two ways to be wrong, and both were paid for once already (ADR-020 A2.1):
// sslmode has to come from the caller, since hard-coding `disable` leaves the
// drill unable to reach a store that requires TLS — the deployment where proving
// recoverability matters most — and it would report that as a failed restore.
// And every value is QUOTED, because an unquoted password containing a space
// parses as a runtime parameter instead, which leaves no host and sends libpq to
// a unix socket inside this process's own container: the drill would then blame
// the restore for a connection it mis-assembled itself.
//
// One connection, so any `SET` a caller makes governs every later statement.
func openPostgres(user, password, host string, port int, database, sslMode string) (*gorm.DB, error) {
	dsn := strings.Join([]string{
		"user=" + rdb.QuoteDSNValue(user),
		"password=" + rdb.QuoteDSNValue(password),
		"host=" + rdb.QuoteDSNValue(host),
		"dbname=" + rdb.QuoteDSNValue(database),
		"port=" + rdb.QuoteDSNValue(strconv.Itoa(port)),
		"sslmode=" + rdb.QuoteDSNValue(sslMode),
	}, " ")
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s@%s:%d/%s: %w", user, host, port, database, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		closeDB(db)
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)
	return db, nil
}

// openEventDB connects to the EVENT store — a different database server from the
// one the secret half reads, on its own port-forward. The two are never the same
// connection: an instance's data lives on two Postgres servers, and a drill that
// silently pointed both halves at one of them would report the other half's
// restore without ever having looked at it.
// Its errors ALREADY CARRY THEIR VERDICT — exitSetup for a connection that could
// not be made, exitTimescaleBroken for a server that answered and is not carrying
// TimescaleDB. Callers return them unwrapped; re-wrapping with failWith would put
// a fresh exitSetup on the outside, and errors.As finds the outermost, so the
// finding would be flattened back into "the drill could not run".
func openEventDB(ctx context.Context, o seedEventsOptions) (*gorm.DB, error) {
	db, err := openPostgres(o.user, o.password, o.host, o.port, o.database, o.sslMode)
	if err != nil {
		return nil, failWith(exitSetup, "%w", err)
	}
	if err := assertTimescale(ctx, db, o.database); err != nil {
		closeDB(db)
		return nil, err
	}
	return db, nil
}

// assertTimescale refuses a database with no TimescaleDB extension loaded.
//
// It is the first thing either event-half command asks, and it is deliberately
// asked BEFORE anything about rows. TimescaleDB registers a custom WAL resource
// manager that has to be loaded at server start, and a recovery bootstrap is
// known to drop `shared_preload_libraries` (cnpg#10840) — which is exactly why
// the platform pins it on the Cluster rather than inheriting it. If that ever
// regresses, every hypertable query below fails with an obscure catalog error,
// and the drill would report a restore problem instead of naming the cause.
func assertTimescale(ctx context.Context, db *gorm.DB, database string) error {
	// 🔴 ErrNoRows is the ONLY error that means "no extension". Every other error
	// means the question was not answered, and answering it with a machinery
	// verdict would be the same defect CHECK 1 in verify_events.go exists to fix:
	// a port-forward dying mid-query would produce exit 6 and a confident factual
	// claim about a database that was never successfully read.
	var version *string
	err := db.WithContext(ctx).Raw(
		`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`).Row().Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows), err == nil && version == nil:
		return failWith(exitTimescaleBroken, `database %q has no timescaledb extension.

The event store is a TimescaleDB cluster; without the extension there are no
hypertables to check and nothing this drill says would be about the data`, database)
	case err != nil:
		return failWith(exitSetup, "could not read the extension list of database %q: %w", database, err)
	}

	// The catalog entry above is DATA — it is restored with everything else, and
	// it survives the failure this check exists for. The library being loaded is
	// the part that is configuration, and the two come apart.
	//
	// 🔴 Measured on a live cluster, by removing timescaledb from
	// shared_preload_libraries and letting CNPG restart in place:
	//
	//   - the Cluster reported "Cluster in healthy state";
	//   - pg_extension still listed timescaledb 2.28.3;
	//   - the API still answered;
	//   - and every row in a COMPRESSED chunk came back as nothing at all. Ten of
	//     twenty seeded measurements, no error, no warning — `SELECT count(*)`
	//     over the compressed range returned 0.
	//
	// Silent partial loss, on a cluster every other signal calls healthy. That is
	// the failure this whole event drill is for, and it is why this question is
	// asked before any question about rows.
	var preloaded string
	if err := db.WithContext(ctx).Raw(`SHOW shared_preload_libraries`).Row().Scan(&preloaded); err != nil {
		return failWith(exitSetup, "reading shared_preload_libraries: %w", err)
	}
	if !strings.Contains(preloaded, "timescaledb") {
		return failWith(exitTimescaleBroken,
			`the timescaledb extension is installed in %q and shared_preload_libraries is %q — it is NOT LOADED.

This is the cnpg#10840 shape, and a recovery bootstrap is known to produce it: the
extension's catalog rehydrates with the data and its resource manager does not get
loaded at server start. Do NOT read a row count from this cluster as a statement
about the backup. Measured on a cluster in exactly this state: compressed chunks
return zero rows, silently, while the Cluster reports healthy and the API answers`,
			database, preloaded)
	}
	fmt.Printf("ok   timescaledb %s is loaded (shared_preload_libraries = %q)\n", *version, preloaded)
	return nil
}

// materializationTable resolves the continuous aggregate's MATERIALIZATION
// hypertable — the physical relation behind measurement_rollups.
//
// 🔴 The event drill's aggregate check goes through here rather than through the
// view, and the reason is the WATERMARK.
//
// measurement_rollups sets `timescaledb.materialized_only = false`, so a read of
// the view serves everything BELOW the materialization watermark out of the
// materialization and recomputes everything ABOVE it live from the raw
// hypertable. Both were measured on a live cluster:
//
//   - rows above the watermark, never materialized: the view returns 5 buckets
//     summing to 25 while the materialization holds 0 for that device;
//   - rows below the watermark, with the materialization emptied by hand: the
//     view returns 0, i.e. it does NOT recompute them.
//
// So a read through the view answers a question about the watermark as much as
// about the data, and the watermark is itself restored state — it comes back with
// everything else. The view cannot say which of the two it found, and the two
// have opposite meanings for a restore. Reading the materialization hypertable
// asks once, about the physical rows, and gets one answer.
//
// The catalog is read rather than the internal name being constructed, because
// `_timescaledb_internal._materialized_hypertable_N` carries a serial N that
// differs between the seeded cluster and the restored one only if something has
// gone wrong — which is exactly the thing under test, and therefore not something
// to assume in the lookup.
func materializationTable(ctx context.Context, db *gorm.DB, schema string) (string, error) {
	// The two outcomes are returned as DIFFERENT errors, and the caller maps them
	// to different exit codes. "The catalog has no materialization for this
	// aggregate" is a finding about the restore; "the query failed" is not a
	// finding at all, and reporting it as one would claim the cluster is broken on
	// the strength of a question that was never answered.
	var qualified string
	err := db.WithContext(ctx).Raw(`
		SELECT format('%I.%I', ht.schema_name, ht.table_name)
		FROM _timescaledb_catalog.continuous_agg ca
		JOIN _timescaledb_catalog.hypertable ht ON ht.id = ca.mat_hypertable_id
		WHERE ca.user_view_schema = ? AND ca.user_view_name = ?`,
		schema, rollupView).Row().Scan(&qualified)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNoMaterialization
	}
	if err != nil {
		return "", fmt.Errorf("resolving the materialization hypertable behind %s.%s: %w", schema, rollupView, err)
	}
	return qualified, nil
}

// errNoMaterialization is the aggregate having no materialization hypertable in
// the catalog — a verdict about the restore, as opposed to a query that failed.
var errNoMaterialization = errors.New("the continuous aggregate has no materialization hypertable in the TimescaleDB catalog")

// materializedRows counts the rows physically present in the materialization
// hypertable, restricted to the drill's own tenant AND DEVICE.
//
// 🔴 The device is part of the scope, not decoration. checkWritable inserts a
// probe measurement into the same tenant to prove the restored cluster accepts
// writes, and the refresh policy runs every 60 seconds — so a tenant-only count
// can pick the probe's bucket up in the window before the probe is deleted, and a
// later verify-events against the same cluster then reports 21 against a recorded
// 20 and calls it broken machinery. A false verdict manufactured by the drill's
// own probe.
func materializedRows(ctx context.Context, db *gorm.DB, table, tenant, device string) (int, error) {
	var n int
	// The table name is interpolated because it comes from format('%I.%I') in the
	// catalog, i.e. already quoted by Postgres itself; the caller-supplied values
	// are bound.
	err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM `+table+` WHERE tenant_id = ? AND device_token = ?`,
		tenant, device).Row().Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting rows in the materialization hypertable %s: %w", table, err)
	}
	return n, nil
}

// hypertableChunks reports how many chunks a hypertable has. A restored
// hypertable with zero chunks holds no data at all, whatever the catalog says
// about its shape — and a hypertable's rows live in chunks that are separate
// physical relations, which is the specific way a partial recovery can leave a
// table that looks present and is not.
func hypertableChunks(ctx context.Context, db *gorm.DB, schema, table string) (int, error) {
	var n int
	err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = ? AND hypertable_name = ?`, schema, table).Row().Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting chunks of %s.%s: %w", schema, table, err)
	}
	return n, nil
}

// isHypertable reports whether the named relation is a hypertable.
//
// It is asked separately from the row counts, and before them, because the two
// failures are different findings. A plain table with the right rows in it means
// the restore rehydrated the DATA and lost the TimescaleDB partitioning — which
// reads as a pass on any count-based check, keeps working until the table is
// large enough to matter, and cannot be undone in place.
func isHypertable(ctx context.Context, db *gorm.DB, schema, table string) (bool, error) {
	var n int
	err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM timescaledb_information.hypertables
		WHERE hypertable_schema = ? AND hypertable_name = ?`, schema, table).Row().Scan(&n)
	if err != nil {
		return false, fmt.Errorf("asking whether %s.%s is a hypertable: %w", schema, table, err)
	}
	return n > 0, nil
}

// timescaleJob is one row of timescaledb_information.jobs, plus the run counter
// that is the only field here capable of proving anything.
type timescaleJob struct {
	ID        int
	Proc      string
	Scheduled bool
	// NextStart and TotalRuns both come from _timescaledb_internal.bgw_job_stat.
	//
	// 🔴 THAT MAKES next_start USELESS ON ITS OWN, and an earlier version of this
	// check rested on it. bgw_job_stat is an ordinary heap table (confirmed by
	// reading the view definition: timescaledb_information.jobs LEFT JOINs it),
	// so a PHYSICAL restore brings it back holding the OLD cluster's numbers. A
	// restored cluster whose background-worker scheduler never started therefore
	// shows every job `scheduled = true` with a non-NULL, stale next_start — and
	// an assertion that next_start is present passes, because the data restored,
	// not because anything will ever run.
	//
	// The shape the old comment claimed to catch — present, scheduled, and
	// permanently WITHOUT a next_start — is what a FRESH cluster with a dead
	// scheduler looks like (no bgw_job_stat row at all, so the LEFT JOIN yields
	// NULL). It is precisely the shape a restored one cannot show.
	//
	// TotalRuns is a monotonic counter, so watching it MOVE observes the scheduler
	// executing on THIS cluster. See schedulerIsRunning.
	NextStart *string
	TotalRuns int64
}

// eventStoreJobs lists the background jobs attached to the event store's own
// objects: the continuous-aggregate refresh policy and the per-hypertable
// compression policies.
//
// 🔑 It deliberately does NOT list every job on the server. Two stock jobs are
// none of this drill's business, and one of them would break it: the image ships
// a telemetry job that the platform DELETES at initdb (post_init_template_sql)
// precisely because `telemetry_level = off` leaves it present, scheduled and
// permanently without a next_start — a check that is red on a working system is a
// check that gets switched off. policy_job_stat_history_retention carries no
// hypertable at all and is likewise filtered out by the predicate below.
//
// Measured on a live instance: the refresh policy reports
// hypertable_schema = 'event-management', hypertable_name = 'measurement_rollups'
// — the USER view, not the materialization hypertable in _timescaledb_internal —
// so this single predicate catches all seven. (An earlier version added
// `OR (proc_schema = '_timescaledb_functions' AND hypertable_schema = ?)` binding
// the SAME schema, which is `A OR (B AND A)` — dead, and dressed as coverage.)
func eventStoreJobs(ctx context.Context, db *gorm.DB, schema string) ([]timescaleJob, error) {
	rows, err := db.WithContext(ctx).Raw(`
		SELECT j.job_id, j.proc_name, j.scheduled, j.next_start::text,
		       coalesce(s.total_runs, 0)
		FROM timescaledb_information.jobs j
		LEFT JOIN timescaledb_information.job_stats s ON s.job_id = j.job_id
		WHERE j.hypertable_schema = ?
		ORDER BY j.job_id`, schema).Rows()
	if err != nil {
		return nil, fmt.Errorf("listing the event store's background jobs: %w", err)
	}
	defer rows.Close()

	var out []timescaleJob
	for rows.Next() {
		var j timescaleJob
		if err := rows.Scan(&j.ID, &j.Proc, &j.Scheduled, &j.NextStart, &j.TotalRuns); err != nil {
			return nil, fmt.Errorf("reading a background job row: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// schedulerIsRunning proves the background-worker scheduler is EXECUTING on this
// cluster, by watching the refresh policy's run counter advance.
//
// This is the only assertion in the jobs check that a physical restore cannot
// satisfy on its own. Everything else about a job — that it exists, that it is
// marked scheduled, that it has a next_start — is restored data.
//
// The refresh policy runs every 60 seconds (schedule_interval = 00:01:00 on a
// stock instance), and its counter was measured advancing in step with that:
// total_runs 132, next_start and last_run_started_at each moving exactly 60s
// across a 75-second observation. So a bound of a little over two intervals is
// generous without being unbounded.
//
// It returns the observed before/after counts so the caller can report what it
// saw rather than only that it was satisfied.
func schedulerIsRunning(ctx context.Context, db *gorm.DB, jobID int) (before, after int64, err error) {
	const (
		interval = 5 * time.Second
		attempts = 30 // ~150s, i.e. two and a half 60-second intervals
	)
	if err := db.WithContext(ctx).Raw(
		`SELECT coalesce(total_runs, 0) FROM timescaledb_information.job_stats WHERE job_id = ?`,
		jobID).Row().Scan(&before); err != nil {
		return 0, 0, fmt.Errorf("reading job %d's run counter: %w", jobID, err)
	}
	after = before
	for i := 0; i < attempts; i++ {
		select {
		case <-ctx.Done():
			return before, after, ctx.Err()
		case <-time.After(interval):
		}
		if err := db.WithContext(ctx).Raw(
			`SELECT coalesce(total_runs, 0) FROM timescaledb_information.job_stats WHERE job_id = ?`,
			jobID).Row().Scan(&after); err != nil {
			return before, after, fmt.Errorf("re-reading job %d's run counter: %w", jobID, err)
		}
		if after > before {
			return before, after, nil
		}
	}
	return before, after, nil
}
