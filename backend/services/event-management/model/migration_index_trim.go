// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// trimmedIndex is this migration's own snapshot of one index it removes: the name
// and the table it sits on, as golden/event-management.sql recorded them before this
// migration. It is a snapshot, not a reference — nothing here reads the live models,
// the baseline, or any constant another file owns.
type trimmedIndex struct {
	name  string // unquoted; %q renders it (four of the names contain a hyphen)
	table string
}

// indexTrimDropped lists the twelve event-store indexes that no query needs, each
// with the index that serves its readers instead:
//
//   - the single-column tenant_id index on five tables. The baseline's snapshot
//     structs inherited it from a gorm `index` tag rather than choosing it, and each
//     is a strict prefix of a composite that leads with tenant_id (the tenant purge's
//     `DELETE ... WHERE tenant_id = ?` is served by those composites).
//   - Timescale's default <table>_occurred_time_idx on every table except
//     measurement_events. Every application read passes the fail-closed tenant
//     callback, so it is served by the table's (tenant_id, occurred_time DESC) index,
//     or by the anchor lookup index on event_anchors. (NewTimeLeadingKeysSchema later
//     drops those tenant-time indexes, whose readers the time-leading identity keys
//     serve; this records what the trim relied on.) measurement_events keeps its own
//     because the measurement_rollups refresh reads that table by time across tenants.
//   - idx_state_change_events_lookup, whose three columns are the leading three of
//     uq_state_change_events_idem.
//   - idx_events_tenant_device_type_time. With event_type between the device and the
//     time it cannot order a device's timeline unless exactly one type is asked for;
//     the per-device read is served in time order by events_device_token_occurred_time_idx.
//
// Every unique index (the idempotency arbiters, which ON CONFLICT infers by column
// list) is kept, so a pod still running the previous version keeps writing correctly
// for the whole of a rolling upgrade.
var indexTrimDropped = []trimmedIndex{
	{"idx_event-management_alert_events_tenant_id", "alert_events"},
	{"idx_event-management_event_anchors_tenant_id", "event_anchors"},
	{"idx_event-management_location_events_tenant_id", "location_events"},
	{"idx_event-management_measurement_events_tenant_id", "measurement_events"},
	{"idx_event-management_state_change_events_tenant_id", "state_change_events"},
	{"alert_events_occurred_time_idx", "alert_events"},
	{"event_anchors_occurred_time_idx", "event_anchors"},
	{"events_occurred_time_idx", "events"},
	{"idx_events_tenant_device_type_time", "events"},
	{"idx_state_change_events_lookup", "state_change_events"},
	{"location_events_occurred_time_idx", "location_events"},
	{"state_change_events_occurred_time_idx", "state_change_events"},
}

// indexTrimTiming bounds how long one drop may hold up the table and how long the
// migration may keep trying.
type indexTrimTiming struct {
	// lockTimeout bounds each single lock wait (SET LOCAL lock_timeout).
	lockTimeout time.Duration
	// statementTimeout bounds the whole attempt, every lock wait included
	// (SET LOCAL statement_timeout). See NewIndexTrimSchema for why both are needed.
	statementTimeout time.Duration
	// pause is the gap between attempts, in which the table's writers run freely.
	pause time.Duration
	// budget bounds the whole migration, every index and every attempt included.
	budget time.Duration
}

// indexTrimDefaultTiming: an attempt holds up the table for at most 5 s; the whole
// migration gives up after a minute, which leaves most of event-management's 150 s
// startup probe (deploy/helm values: 5 s × 30) for the rest of startup.
//
// The 5 s statement bound has room for a real instance: on the pinned TimescaleDB image
// an uncontended DROP INDEX on a hypertable of 1,000 daily chunks — nearly three years
// with retention off, the default — took 0.54 s.
var indexTrimDefaultTiming = indexTrimTiming{
	lockTimeout:      3 * time.Second,
	statementTimeout: 5 * time.Second,
	pause:            2 * time.Second,
	budget:           60 * time.Second,
}

// indexTrimBusyMessage is the error a busy table ends the migration with. It is
// written for the operator who reads it in the pod's log: what happened, that nothing
// is half-applied, and how to find the holder (indexTrimHolderQuery).
const indexTrimBusyMessage = "could not drop index %s on \"event-management\".%s: the table stayed busy on every attempt " +
	"(%d attempts, each bounded to %s, within %s). Nothing is half-applied: indexes already dropped stay dropped, " +
	"and this migration continues from here on the next start. The previous event-management keeps storing events " +
	"meanwhile. Find the transaction holding the table with: %s; last error: %w"

// indexTrimHolderQuery is the query the busy error hands the operator: every session
// holding a lock on the table OR on any of its chunks. A holder of chunks alone — a
// direct chunk reader, the compression or retention job — never locks the hypertable's
// own oid, so matching that oid alone would find nothing for exactly the case
// TestIntegrationIndexTrimBoundsAnAttemptWhenChunksAreHeld builds.
func indexTrimHolderQuery(table string) string {
	return "SELECT pid, state, xact_start, left(query, 200) FROM pg_stat_activity WHERE pid IN " +
		"(SELECT pid FROM pg_locks WHERE granted AND (relation = '\"event-management\"." + table + "'::regclass " +
		"OR relation IN (SELECT format('%I.%I', chunk_schema, chunk_name)::regclass " +
		"FROM timescaledb_information.chunks WHERE hypertable_schema = 'event-management' " +
		"AND hypertable_name = '" + table + "')))"
}

// indexTrimLockTableFullMessage names the one non-retryable failure an operator can fix
// with a setting: one DROP locks the hypertable and every one of its chunks, so an
// instance with many chunks can exhaust the lock table.
const indexTrimLockTableFullMessage = "drop index %s on \"event-management\".%s: the server ran out of lock slots " +
	"(one drop locks the table and every chunk of it). Raise max_locks_per_transaction on the event store and " +
	"restart; this migration continues from here. Error: %w"

// NewIndexTrimSchema drops the twelve event-store indexes no query needs (see
// indexTrimDropped). It builds nothing: an index BUILD over live chunks would hold a
// SHARE lock against ingest for the whole build, and could outlast the startup probe.
//
// The properties below are load-bearing:
//
//  1. **Each DROP runs in its own short transaction, under SET LOCAL lock_timeout and
//     SET LOCAL statement_timeout.** DROP INDEX takes ACCESS EXCLUSIVE on the
//     hypertable, and then on each chunk as Timescale drops the chunk's copy. Queued
//     behind a long reader — the measurement_rollups refresh, a BI query, the
//     compression job, a tenant purge (one transaction over every table) — an
//     unbounded DROP would block every later read AND write of that table for the
//     reader's whole duration. lock_timeout bounds each single lock wait; it does NOT
//     bound the attempt, because holders of chunk locks (but not of the hypertable) make
//     the attempt wait once per chunk while it already holds the hypertable. Measured on
//     the pinned image: three chunk holders letting go 400 ms apart, under a 500 ms
//     lock_timeout, held the hypertable — and a write to a fourth, untouched chunk — for
//     1.2 s. statement_timeout bounds the whole attempt
//     (TestIntegrationIndexTrimBoundsAnAttemptWhenChunksAreHeld). Both are SET LOCAL, so
//     they end with the transaction and no pooled connection leaves with either — core
//     rejects a global lock_timeout on purpose (it would kill the purge sweep and this
//     very DDL). SET LOCAL outside a transaction is a no-op with a warning, which is why
//     the Transaction wrapper is load-bearing even though migrations run with
//     UseTransaction:false.
//
//     The package's general rule is that this area's DDL is non-transactional because
//     Timescale forbids it; that holds for create_hypertable and the policy calls, not
//     for DROP INDEX on a hypertable, which Timescale runs inside a transaction block.
//     TestIntegrationIndexTrimWaitsBoundedlyForALockAndResumes runs it that way against
//     the pinned image and proves the bound; do not "fix" the wrapper away.
//
//  2. **Only a busy table is retried** (isRetryableDropFailure): lock_not_available,
//     deadlock_detected, and the statement_timeout cancel. Anything else — Timescale
//     refusing the DDL, a lost connection, the lock table filling up — fails at once,
//     never retried into a minute of silence.
//
//  3. **Exhausting the budget fails the migration, and the service exits.** Skipping
//     the index instead would record the migration as applied with the index still
//     present — a silent partial apply. The previous pod keeps serving
//     (maxUnavailable: 0), and the next start resumes: DROP INDEX IF EXISTS on an index
//     already gone takes no table lock, so progress is monotone.
//
//  4. **It is individually re-runnable.** IF EXISTS on every statement, and nothing
//     depends on an earlier statement of the same pass having run.
//
// There is deliberately no Rollback: re-creating these indexes is the non-concurrent
// index build over live chunks that this migration exists to avoid, and gormigrate
// reports ErrRollbackImpossible for a nil Rollback, which fails loudly. The DDL of
// each dropped index is in the golden schema's history.
//
// No CONCURRENTLY: Timescale does not support it on a hypertable, and the plain form
// removes catalog entries and files without scanning a row.
func NewIndexTrimSchema() *gormigrate.Migration {
	return newIndexTrimSchema(indexTrimDefaultTiming)
}

func newIndexTrimSchema(timing indexTrimTiming) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260930000000",
		Migrate: func(tx *gorm.DB) error {
			deadline := time.Now().Add(timing.budget)
			for _, idx := range indexTrimDropped {
				if err := dropIndexBounded(tx, idx, timing, deadline); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// dropIndexBounded drops one index in its own transaction under SET LOCAL lock_timeout
// and statement_timeout, retrying a busy table until deadline.
func dropIndexBounded(db *gorm.DB, idx trimmedIndex, timing indexTrimTiming, deadline time.Time) error {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 1; ; attempt++ {
		err := db.Transaction(func(t *gorm.DB) error {
			if err := t.Exec(fmt.Sprintf(`SET LOCAL lock_timeout = '%dms'`,
				timing.lockTimeout.Milliseconds())).Error; err != nil {
				return err
			}
			if err := t.Exec(fmt.Sprintf(`SET LOCAL statement_timeout = '%dms'`,
				timing.statementTimeout.Milliseconds())).Error; err != nil {
				return err
			}
			return t.Exec(fmt.Sprintf(`DROP INDEX IF EXISTS "event-management".%q`, idx.name)).Error
		})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			// The migration's own context ended; a cancel it caused is not a busy table.
			return fmt.Errorf("drop index %s on \"event-management\".%s: %w", idx.name, idx.table, ctx.Err())
		}
		if final := dropFailureError(idx, err); final != nil {
			return final
		}
		// Start another attempt only if it can end, in the worst case, by the deadline.
		if time.Now().Add(timing.pause + timing.statementTimeout).After(deadline) {
			return fmt.Errorf(indexTrimBusyMessage, idx.name, idx.table, attempt, timing.statementTimeout,
				timing.budget, indexTrimHolderQuery(idx.table), err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("drop index %s on \"event-management\".%s: %w", idx.name, idx.table, ctx.Err())
		case <-time.After(timing.pause):
		}
	}
}

// dropFailureError decides what one failed attempt means: nil for a busy table, which
// the caller retries, or the error that ends the migration. The lock table filling up
// gets its own message, because it is the one failure the operator fixes with a setting.
// The loop returns whatever this returns, so TestDropFailureError pins the message the
// operator sees for each failure, not only the helpers that classify it.
func dropFailureError(idx trimmedIndex, err error) error {
	switch {
	case isLockTableFull(err):
		return fmt.Errorf(indexTrimLockTableFullMessage, idx.name, idx.table, err)
	case isRetryableDropFailure(err):
		return nil
	default:
		return fmt.Errorf("drop index %s on \"event-management\".%s: %w", idx.name, idx.table, err)
	}
}

// isRetryableDropFailure reports whether a DROP failed only because another
// transaction held the table: lock_not_available (55P03, lock_timeout fired),
// deadlock_detected (40P01), or query_canceled (57014, statement_timeout fired). The
// caller checks its own context first, so a cancel it caused is never retried.
func isRetryableDropFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "55P03", "40P01", "57014":
		return true
	}
	return false
}

// isLockTableFull reports out_of_memory (53200), which Postgres raises when the shared
// lock table is exhausted — not retryable, but fixable by the operator.
func isLockTableFull(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "53200"
}
