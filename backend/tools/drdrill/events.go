// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	var version *string
	if err := db.WithContext(ctx).Raw(
		`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`).Row().Scan(&version); err != nil || version == nil {
		return failWith(exitTimescaleBroken, `database %q has no timescaledb extension.

The event store is a TimescaleDB cluster; without the extension there are no
hypertables to check and nothing this drill says would be about the data`, database)
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
	var qualified string
	err := db.WithContext(ctx).Raw(`
		SELECT format('%I.%I', ht.schema_name, ht.table_name)
		FROM _timescaledb_catalog.continuous_agg ca
		JOIN _timescaledb_catalog.hypertable ht ON ht.id = ca.mat_hypertable_id
		WHERE ca.user_view_schema = ? AND ca.user_view_name = ?`,
		schema, rollupView).Row().Scan(&qualified)
	if err != nil {
		return "", fmt.Errorf("resolving the materialization hypertable behind %s.%s: %w", schema, rollupView, err)
	}
	if qualified == "" {
		return "", fmt.Errorf("%s.%s has no materialization hypertable in the TimescaleDB catalog", schema, rollupView)
	}
	return qualified, nil
}

// materializedRows counts the rows physically present in the materialization
// hypertable, restricted to the drill's own tenant so a shared cluster cannot
// lend the count someone else's data.
func materializedRows(ctx context.Context, db *gorm.DB, table, tenant string) (int, error) {
	var n int
	// The table name is interpolated because it comes from format('%I.%I') in the
	// catalog, i.e. already quoted by Postgres itself; the tenant, which is the
	// only caller-supplied value, is bound.
	err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM `+table+` WHERE tenant_id = ?`, tenant).Row().Scan(&n)
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

// timescaleJob is one row of timescaledb_information.jobs.
type timescaleJob struct {
	ID        int
	Proc      string
	Scheduled bool
	// NextStart is null for a job the scheduler has not planned. A restored
	// cluster whose background workers never started shows exactly this: the
	// job rows are physically present (they came back with the data) and nothing
	// is ever going to run them.
	NextStart *string
}

// eventStoreJobs lists the background jobs attached to the event store's own
// objects: the continuous-aggregate refresh policy, plus any retention policy
// the data-lifecycle reconciler has installed.
//
// 🔑 It deliberately does NOT list every job on the server. The stock TimescaleDB
// image ships a telemetry job, and the platform DELETES it at initdb
// (post_init_template_sql) precisely because `telemetry_level = off` leaves the
// job present, scheduled and permanently without a next_start — the exact shape
// this check reads as "the scheduler is stuck", on a healthy cluster, forever.
// A check that is red on a working system is a check that gets switched off.
func eventStoreJobs(ctx context.Context, db *gorm.DB, schema string) ([]timescaleJob, error) {
	rows, err := db.WithContext(ctx).Raw(`
		SELECT job_id, proc_name, scheduled, next_start::text
		FROM timescaledb_information.jobs
		WHERE hypertable_schema = ? OR (proc_schema = '_timescaledb_functions' AND hypertable_schema = ?)
		ORDER BY job_id`, schema, schema).Rows()
	if err != nil {
		return nil, fmt.Errorf("listing the event store's background jobs: %w", err)
	}
	defer rows.Close()

	var out []timescaleJob
	for rows.Next() {
		var j timescaleJob
		if err := rows.Scan(&j.ID, &j.Proc, &j.Scheduled, &j.NextStart); err != nil {
			return nil, fmt.Errorf("reading a background job row: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
