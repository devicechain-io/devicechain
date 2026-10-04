// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// tenantDeviceIndexSnapshot is this migration's own snapshot: the index it builds, exactly as
// pg_get_indexdef renders it, and the index it replaces, as golden/event-management.sql recorded
// it before this migration. Nothing here reads the baseline, the live models or another
// migration's tables.
type tenantDeviceIndexSnapshot struct {
	table    string // in "event-management"
	name     string
	columns  string // as written in CREATE INDEX
	def      string // pg_get_indexdef of a correct index
	replaces string
}

var tenantDeviceIndex = tenantDeviceIndexSnapshot{
	table:    "events",
	name:     "idx_events_tenant_device_time",
	columns:  "tenant_id, device_token, occurred_time DESC",
	def:      `CREATE INDEX idx_events_tenant_device_time ON "event-management".events USING btree (tenant_id, device_token, occurred_time DESC)`,
	replaces: "events_device_token_occurred_time_idx",
}

// tenantDeviceIndexManualDrop and tenantDeviceIndexManualBuild are the two statements every
// refusal hands the operator. The build is chunk by chunk (timescaledb.transaction_per_chunk),
// which a transaction cannot be: each chunk is indexed and committed on its own, so a write
// waits only while the chunk it targets is being indexed, not for the whole table. Measured on
// the pinned image, TimescaleDB 2.28.3, it is accepted on a hypertable that has compressed
// chunks. It has no IF NOT EXISTS on purpose: an index of that name already there, including
// the INVALID one an interrupted build leaves, must fail the statement loudly rather than be
// skipped as though it were built. The DROP first removes exactly that leftover, and is a no-op
// when there is none.
const (
	tenantDeviceIndexManualDrop  = `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_time`
	tenantDeviceIndexManualBuild = `CREATE INDEX idx_events_tenant_device_time ` +
		`ON "event-management".events (tenant_id, device_token, occurred_time DESC) ` +
		`WITH (timescaledb.transaction_per_chunk)`
)

const tenantDeviceIndexTooSlowMarker = "devicechain:tenant-device-index-too-slow"

// tenantDeviceIndexManualAdvice is what every refusal that leaves the choice to the operator
// says, ending without a full stop so a message can continue it. None of them asks for a
// recreate: the schema is correct without this index, only slower.
const tenantDeviceIndexManualAdvice = "To finish it in place, run these on the event store's primary, outside " +
	"event-management's startup and at a quiet time: " + tenantDeviceIndexManualDrop + "; " +
	tenantDeviceIndexManualBuild + ". The build goes chunk by chunk, so writes to a chunk wait only while that " +
	"chunk is indexed (about 3 microseconds for each of its rows not yet compressed). If it is interrupted it " +
	"leaves an invalid index of that name, which the DROP removes before the next try. Then restart " +
	"event-management, which removes the index it replaces. The same statements can be run before upgrading"

const tenantDeviceIndexHistoryMessage = "leading the per-device event index with the tenant: more than %d rows of " +
	"\"event-management\".events not yet compressed would have to be indexed, longer than event-management may " +
	"spend starting, so this start changed nothing. " + tenantDeviceIndexManualAdvice +
	". The previous event-management keeps storing events meanwhile."

const tenantDeviceIndexChunksMessage = "leading the per-device event index with the tenant: %s more than %d chunks, " +
	"the most this release builds an index over within one start, so this start changed nothing. " +
	tenantDeviceIndexManualAdvice + ". If the store reports it is out of lock slots, raise " +
	"max_locks_per_transaction first. List the chunks with: %s. The previous event-management keeps storing " +
	"events meanwhile."

const tenantDeviceIndexChunkCountFailedMessage = "leading the per-device event index with the tenant: counting the " +
	"chunks of events failed, so nothing was changed; the next start tries again. If it fails the same way on " +
	"every start, list the chunks with: %s. Error: %w"

const tenantDeviceIndexRowCountFailedMessage = "leading the per-device event index with the tenant: counting the " +
	"rows to index failed, so nothing was changed; the next start tries again. Error: %w"

const tenantDeviceIndexTooSlowMessage = "building %s on \"event-management\".events did not finish within %s once " +
	"the table was locked, so it was rolled back and the table keeps %s. A build this slow would hold writes on " +
	"every start, so it is not retried: event-management now stops at once on every start until the index " +
	"exists. " + tenantDeviceIndexManualAdvice + ". To let a start try once more instead, clear the marker with: " +
	"COMMENT ON INDEX \"event-management\".%s IS NULL. Error: %w"

const tenantDeviceIndexRefusedMessage = "building %s on \"event-management\".events was refused: an earlier start " +
	"found it too slow and marked %s (%q). Nothing was locked or changed. " + tenantDeviceIndexManualAdvice +
	". To let a start try once more instead, clear the marker with: COMMENT ON INDEX \"event-management\".%s IS NULL."

const tenantDeviceIndexBusyMessage = "could not lock \"event-management\".events to build %s: the table stayed busy " +
	"on every attempt (%d attempts, each bounded to %s, within %s). Nothing was changed, and this migration " +
	"continues from here on the next start. The previous event-management keeps storing events meanwhile. " +
	"Find the transaction holding the table with: %s; last error: %w"

const tenantDeviceIndexOutOfTimeMessage = "building %s on \"event-management\".events stopped because this start's " +
	"%s was spent. Nothing is half-applied, and this migration continues from here on the next start."

const tenantDeviceIndexLockTableFullMessage = "building %s on \"event-management\".events: the server ran out of " +
	"lock slots (the build locks the table and every chunk of it). Raise max_locks_per_transaction on the event " +
	"store and restart; nothing was changed. Error: %w"

const tenantDeviceIndexFailedMessage = "building %s on \"event-management\".events failed and was rolled back; the " +
	"next start tries again. If it fails the same way on every start: " + tenantDeviceIndexManualAdvice +
	". Error: %w"

const tenantDeviceIndexShapeMessage = "an index named %s exists on \"event-management\" as (%s), which is not the " +
	"index this migration builds (%s). Nothing was changed: this migration does not replace an index it did not " +
	"write. Drop or rename it, then restart event-management."

const tenantDeviceIndexMarkerFailedNote = "%w (and the marker that would refuse the next start could not be written, " +
	"so every start will try the build again until the index exists: %v)"

// afterDeviceIndexLock, when set, is called by the build once the table is locked and before
// the index is created. It is nil in production. The integration tests use it to hold the
// build at the one moment its lock is held, so that what other sessions can and cannot do
// then is observed rather than raced against a build that takes well under a second.
var afterDeviceIndexLock func()

// NewTenantDeviceIndexSchema leads the per-device event index with the tenant: it builds
// (tenant_id, device_token, occurred_time DESC) on events and then drops
// events_device_token_occurred_time_idx, which carried no tenant. The read that pays for the
// old index is the device's event list: the fail-closed tenant predicate was checked row by
// row, so the list's total, and a device's list filtered by event type, visited every
// uncompressed row carrying the token in ANY tenant.
//
// Each stored base event updates the same number of indexes as before: one is swapped for
// one, a little wider (the tenant id).
//
// The properties below are load-bearing:
//
//  1. **It reads the catalog first and replaces only what it recognises.** An index of the new
//     name that is not the one this migration builds, as far as the shape check sees ends it before any DDL. Done
//     (new valid, old gone) takes no lock at all, which is what makes a re-run free.
//  2. **Gates before any lock**, as for the key rebuild and with the same bounds: chunks (from
//     the catalog), then the heap rows of events not yet compressed. Past either it stops,
//     changing nothing, and hands the operator the build to run by hand. It does NOT ask for a
//     recreate: this is a performance index on a schema that is otherwise correct.
//  3. **The build is ONE transaction under LOCK ... IN SHARE MODE** (writers wait, readers do
//     not), under SET LOCAL lock_timeout and statement_timeout, retried while the table is
//     busy. Every SET is SET LOCAL inside db.Transaction, so nothing outlives it on a pooled
//     connection. It does not use timescaledb.transaction_per_chunk: that cannot run inside a
//     transaction block, so neither bound would apply, and an interrupted run leaves an INVALID
//     index. The hand-run remedy does use it, where those two costs are the operator's to
//     choose; an INVALID leftover of either is dropped here before the build.
//  4. **The old index is dropped in its own short transaction** (dropIndexBounded), never in
//     the build's: a SHARE lock cannot be upgraded to the ACCESS EXCLUSIVE a DROP needs
//     without queueing behind every reader, and the new index must be committed first. It is
//     not chunk-gated, deliberately and unlike the trim: after a chunk refusal the remedy is the
//     manual build and a restart, which takes this path, and gating it would refuse the
//     operator who followed the remedy. An uncontended drop over 1,000 chunks took 0.54 s of
//     its 5 s bound, and a full lock table has its own message.
//  5. **"Too slow" is decided once, and sticks, on the OLD index** (the new one does not exist
//     after the rollback). A VALID new index wins over the marker, so the manual remedy needs
//     no clearing step. If the old index is already gone no marker can be written, and the
//     error says every start will then try again: accepted, because that needs an operator to
//     have dropped it.
//  6. **It is individually re-runnable**, and the order is load-bearing: the new index is valid
//     before the old one goes, so the device read always has an index.
//
// There is no Rollback, as for the trim: gormigrate reports ErrRollbackImpossible for a nil
// one, and the old index is the same build again. A pod still running the previous release
// works throughout and after a downgrade: the old index is no conflict arbiter (events'
// writers infer events_pkey and idx_events_tenant_alt_id), and the previous release's
// tenant-scoped reads use the new index. There is no CONCURRENTLY: Timescale does not support
// it on a hypertable.
//
// The bounds are the key rebuild's own value, not a copy: the upgrade notes publish one row
// bound and one chunk bound for both.
//
// The budget is this migration's own fresh one (60 s), taken when it starts. An upgrade
// straight from v0.18.0 runs the index trim, the key rebuild and this migration in one start,
// each with its own budget, so the worst case is about three budgets plus the rest of startup,
// against the startup probe. That is a restart, not a stall: a killed build rolls back
// server-side, the earlier migrations are already recorded, and no marker is written, so the
// next start resumes here.
func NewTenantDeviceIndexSchema() *gormigrate.Migration {
	return newTenantDeviceIndexSchema(timeLeadingKeysDefaultTiming)
}

func newTenantDeviceIndexSchema(timing timeLeadingKeysTiming) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261004000000",
		Migrate: func(tx *gorm.DB) error {
			return leadDeviceIndexWithTenant(tx, timing)
		},
	}
}

type deviceIndexState int

const (
	// Done and DropOld are told apart for the log and the tests only: the caller runs the same
	// bounded DROP INDEX IF EXISTS of the old index in both. "Done takes no lock" therefore
	// rests on DROP INDEX IF EXISTS of a missing name resolving no relation and so locking
	// nothing, which the re-run test under an ACCESS EXCLUSIVE holder pins.
	deviceIndexDone    deviceIndexState = iota // new valid, old absent
	deviceIndexDropOld                         // new valid, old present
	deviceIndexBuild                           // new absent
	deviceIndexRebuild                         // new present, the right definition, INVALID: a leftover
)

func leadDeviceIndexWithTenant(db *gorm.DB, timing timeLeadingKeysTiming) error {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timing.budget)

	state, err := classifyDeviceIndex(db)
	if err != nil {
		return err
	}
	if state == deviceIndexBuild || state == deviceIndexRebuild {
		if err := gateDeviceIndex(db, timing); err != nil {
			return err
		}
		if state == deviceIndexRebuild {
			// After the gates, so a refusal changes nothing. The leftover serves no read.
			if err := dropIndexBounded(db, trimmedIndex{tenantDeviceIndex.name, tenantDeviceIndex.table},
				dropTiming(timing), deadline); err != nil {
				return err
			}
		}
		if err := buildDeviceIndexWithRetry(ctx, db, timing, deadline); err != nil {
			return err
		}
	}
	if err := dropIndexBounded(db, trimmedIndex{tenantDeviceIndex.replaces, tenantDeviceIndex.table},
		dropTiming(timing), deadline); err != nil {
		return fmt.Errorf("leading the per-device event index with the tenant: %s is built; removing %s failed: %w",
			tenantDeviceIndex.name, tenantDeviceIndex.replaces, err)
	}
	return nil
}

// dropTiming maps the build timing onto the trim's drop bounds.
func dropTiming(timing timeLeadingKeysTiming) indexTrimTiming {
	return indexTrimTiming{
		lockTimeout:      timing.lockTimeout,
		statementTimeout: timing.lockAttempt,
		pause:            timing.pause,
		budget:           timing.budget,
		maxChunks:        timing.maxChunks,
		countTimeout:     timing.countTimeout,
	}
}

// gateDeviceIndex refuses, changing nothing and locking nothing, a table with more chunks than
// maxChunks or more rows not yet compressed than maxRows. The chunk count comes first: it is
// one catalog read, while the row count is a statement per chunk. Both are of events alone.
func gateDeviceIndex(db *gorm.DB, timing timeLeadingKeysTiming) error {
	tables := []string{tenantDeviceIndex.table}
	counts, err := chunksPerTable(db, tables, timing.countTimeout)
	if err != nil {
		return fmt.Errorf(tenantDeviceIndexChunkCountFailedMessage, chunkListQuery, err)
	}
	if over := overChunkCeiling(counts, tables, timing.maxChunks); over != "" {
		return fmt.Errorf(tenantDeviceIndexChunksMessage, over, timing.maxChunks, chunkListQuery)
	}
	rows, err := rowsToRekey(db, tables, timing.maxRows, timing.countTimeout)
	if err != nil {
		return fmt.Errorf(tenantDeviceIndexRowCountFailedMessage, err)
	}
	if rows > timing.maxRows {
		return fmt.Errorf(tenantDeviceIndexHistoryMessage, timing.maxRows)
	}
	return nil
}

// classifyDeviceIndex reads the catalog only. A new index that is not the one this
// migration builds (columns, DESC bits, method, uniqueness, plainness; null ordering, operator
// class and collation are not compared, the columns being NOT NULL) is refused; a still-needed build is refused when the old index carries the
// too-slow marker, unless a valid new index exists, which makes the marker moot.
func classifyDeviceIndex(db *gorm.DB) (deviceIndexState, error) {
	// The shape is read from pg_index and pg_attribute, never through pg_get_indexdef: that
	// function opens the table, so a re-run would queue behind whoever holds it, and "done
	// takes no lock" is a property of this migration. The signature renders each key column
	// with its DESC bit, as the definition does.
	var found []struct {
		Signature sql.NullString
		Method    sql.NullString
		Unique    sql.NullBool
		Plain     sql.NullBool
		Valid     sql.NullBool
		OnEvents  sql.NullBool
	}
	if err := db.Raw(`SELECT string_agg(a.attname || CASE WHEN k.opt & 1 = 1 THEN ' DESC' ELSE '' END,
			', ' ORDER BY k.ord) AS signature,
		max(am.amname) AS method, bool_and(x.indisunique) AS "unique",
		bool_and(x.indpred IS NULL AND x.indexprs IS NULL AND x.indnkeyatts = x.indnatts) AS plain,
		bool_and(x.indisvalid) AS valid, bool_and(t.relname = ?) AS on_events
		FROM pg_class i
		JOIN pg_index x ON x.indexrelid = i.oid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_am am ON am.oid = i.relam
		JOIN pg_namespace n ON n.oid = i.relnamespace
		CROSS JOIN LATERAL unnest(x.indkey::int2[], x.indoption::int2[]) WITH ORDINALITY AS k(attnum, opt, ord)
		LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
		WHERE n.nspname = 'event-management' AND i.relname = ?
		GROUP BY i.oid`,
		tenantDeviceIndex.table, tenantDeviceIndex.name).Scan(&found).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", tenantDeviceIndex.name, err)
	}
	var old struct {
		Present bool
		Note    sql.NullString
	}
	if err := db.Raw(`SELECT to_regclass(format('%I.%I', 'event-management', ?::text)) IS NOT NULL AS present,
		obj_description(to_regclass(format('%I.%I', 'event-management', ?::text)), 'pg_class') AS note`,
		tenantDeviceIndex.replaces, tenantDeviceIndex.replaces).Scan(&old).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", tenantDeviceIndex.replaces, err)
	}

	valid := false
	if len(found) > 0 {
		f := found[0]
		if f.Signature.String != tenantDeviceIndex.columns || f.Method.String != "btree" || f.Unique.Bool ||
			!f.Plain.Bool || !f.OnEvents.Bool {
			return 0, fmt.Errorf(tenantDeviceIndexShapeMessage, tenantDeviceIndex.name,
				fmt.Sprintf("%s%s on %s", f.Signature.String, map[bool]string{true: ", unique", false: ""}[f.Unique.Bool],
					map[bool]string{true: "events", false: "another table"}[f.OnEvents.Bool]),
				tenantDeviceIndex.columns)
		}
		valid = f.Valid.Bool
	}
	switch {
	case valid && old.Present:
		return deviceIndexDropOld, nil
	case valid:
		return deviceIndexDone, nil
	}
	if old.Note.Valid && strings.HasPrefix(old.Note.String, tenantDeviceIndexTooSlowMarker) {
		return 0, fmt.Errorf(tenantDeviceIndexRefusedMessage, tenantDeviceIndex.name, tenantDeviceIndex.replaces,
			old.Note.String, tenantDeviceIndex.replaces)
	}
	if len(found) > 0 {
		return deviceIndexRebuild, nil
	}
	return deviceIndexBuild, nil
}

// buildDeviceIndexWithRetry builds the index, retrying a busy table until the deadline. It is
// rekeyWithRetry for one index: the same checks before every attempt, the same pause.
func buildDeviceIndexWithRetry(ctx context.Context, db *gorm.DB, timing timeLeadingKeysTiming,
	deadline time.Time) error {
	for attempt := 1; ; attempt++ {
		if time.Now().Add(timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf(tenantDeviceIndexOutOfTimeMessage, tenantDeviceIndex.name,
				"budget of "+timing.budget.String())
		}
		failed, err := buildDeviceIndex(db, timing, deadline)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("build %s on \"event-management\".events: %w", tenantDeviceIndex.name, ctx.Err())
		}
		final, tooSlow := deviceIndexFailureError(failed, err, timing)
		if tooSlow {
			if markErr := markDeviceIndexTooSlow(db, timing); markErr != nil {
				return fmt.Errorf(tenantDeviceIndexMarkerFailedNote, final, markErr)
			}
		}
		if final != nil {
			return final
		}
		if time.Now().Add(timing.pause + timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf(tenantDeviceIndexBusyMessage, tenantDeviceIndex.name, attempt, timing.lockAttempt,
				timing.budget, indexTrimHolderQuery(tenantDeviceIndex.table), err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("build %s on \"event-management\".events: %w", tenantDeviceIndex.name, ctx.Err())
		case <-time.After(timing.pause):
		}
	}
}

// buildDeviceIndex builds the index inside ONE transaction: lock events, every chunk and their
// compressed relations IN SHARE MODE under a bounded wait, then create the index under what is
// left of the build allowance. It reports, on failure, what the attempt got to.
func buildDeviceIndex(db *gorm.DB, timing timeLeadingKeysTiming, deadline time.Time) (rekeyAttempt, error) {
	attempt := rekeyAttempt{phase: rekeyLocking}
	err := db.Transaction(func(t *gorm.DB) error {
		lockAttempt, err := timeoutSetting(timing.lockAttempt)
		if err != nil {
			return err
		}
		lockTimeout, err := timeoutSetting(timing.lockTimeout)
		if err != nil {
			return err
		}
		for _, s := range []string{
			`SET LOCAL lock_timeout = '` + lockTimeout + `'`,
			`SET LOCAL statement_timeout = '` + lockAttempt + `'`,
			`SET LOCAL maintenance_work_mem = '` + timing.buildMemory + `'`,
		} {
			if err := t.Exec(s).Error; err != nil {
				return err
			}
		}
		// The compressed relations of the compressed chunks, locked in the SAME statement as
		// the table so one lockAttempt bounds the whole wait (see rekeyTable).
		var compressed []string
		if err := t.Raw(`SELECT format('%I.%I', cc.schema_name, cc.table_name)
			FROM _timescaledb_catalog.chunk c
			JOIN _timescaledb_catalog.chunk cc ON cc.id = c.compressed_chunk_id
			JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
			WHERE h.schema_name = 'event-management' AND h.table_name = ?`,
			tenantDeviceIndex.table).Scan(&compressed).Error; err != nil {
			return err
		}
		targets := append([]string{`"event-management".` + tenantDeviceIndex.table}, compressed...)
		if err := t.Exec(`LOCK TABLE ` + strings.Join(targets, ", ") + ` IN SHARE MODE`).Error; err != nil {
			return err
		}
		if afterDeviceIndexLock != nil {
			afterDeviceIndexLock()
		}

		attempt.phase = rekeyBuilding
		allowance, full, err := buildAllowance(timing, deadline, time.Now())
		if err != nil {
			return err
		}
		attempt.full = full
		build, err := timeoutSetting(allowance)
		if err != nil {
			return err
		}
		if err := t.Exec(`SET LOCAL statement_timeout = '` + build + `'`).Error; err != nil {
			return err
		}
		began := time.Now()
		if err := t.Exec(fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON "event-management".%s (%s)`,
			tenantDeviceIndex.name, tenantDeviceIndex.table, tenantDeviceIndex.columns)).Error; err != nil {
			// statement_timeout bounds the statement; the server's clock starts after this
			// one, so an elapsed time under the allowance cannot be its timeout.
			attempt.ranOut = time.Since(began) >= allowance
			return err
		}
		return nil
	})
	return attempt, err
}

// deviceIndexFailureError decides what one failed attempt means: nil for a busy table, which
// the caller retries, or the error that ends the migration, and whether that error is the
// too-slow verdict the caller makes stick. It is the key rebuild's table
// (timeLeadingKeysFailureError); TestDeviceIndexFailureErrorAgreesWithTheKeyRebuild holds the
// two to the same verdict for every case.
//
//	phase     SQLSTATE                 outcome
//	any       53200 (lock table full)  stop; names max_locks_per_transaction
//	locking   55P03, 40P01, 57014      retry: another session holds the table, a chunk or a compressed relation
//	building  55P03, 40P01             retry: an internal relation was busy
//	building  57014, ran out, whole    stop for good: too slow (sticky)
//	building  57014, ran out, less     stop; resumes: this start's budget was spent
//	building  57014, did not run out   a cancel (pg_cancel_backend): as anything else, below
//	any       errRekeyOutOfTime        stop; resumes
//	any       anything else            stop; retried next start
func deviceIndexFailureError(attempt rekeyAttempt, err error, timing timeLeadingKeysTiming) (final error, tooSlow bool) {
	var pgErr *pgconn.PgError
	isPg := errors.As(err, &pgErr)
	name := tenantDeviceIndex.name
	switch {
	case isLockTableFull(err):
		return fmt.Errorf(tenantDeviceIndexLockTableFullMessage, name, err), false
	case errors.Is(err, errRekeyOutOfTime):
		return fmt.Errorf(tenantDeviceIndexOutOfTimeMessage, name, "budget of "+timing.budget.String()), false
	case isPg && (pgErr.Code == "55P03" || pgErr.Code == "40P01"):
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.phase == rekeyLocking:
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.ranOut && attempt.full:
		return fmt.Errorf(tenantDeviceIndexTooSlowMessage, name, timing.tableBuild, tenantDeviceIndex.replaces,
			tenantDeviceIndex.replaces, err), true
	case isPg && pgErr.Code == "57014" && attempt.ranOut:
		return fmt.Errorf(tenantDeviceIndexOutOfTimeMessage, name, "budget of "+timing.budget.String()), false
	default:
		return fmt.Errorf(tenantDeviceIndexFailedMessage, name, err), false
	}
}

// markDeviceIndexTooSlow leaves the too-slow marker on the OLD index, in a short transaction
// of its own (the build's was rolled back). COMMENT takes SHARE UPDATE EXCLUSIVE on the index,
// which ingest's inserts do not conflict with; the lock_timeout only guards against another
// DDL holding it. If the old index is gone the COMMENT fails and the caller says so.
func markDeviceIndexTooSlow(db *gorm.DB, timing timeLeadingKeysTiming) error {
	note := fmt.Sprintf("%s: building %s did not finish within %s on %s; event-management refuses to retry it. "+
		"Clear this comment to try again.", tenantDeviceIndexTooSlowMarker, tenantDeviceIndex.name,
		timing.tableBuild, time.Now().UTC().Format(time.RFC3339))
	lockTimeout, err := timeoutSetting(timing.lockTimeout)
	if err != nil {
		return err
	}
	return db.Transaction(func(t *gorm.DB) error {
		if err := t.Exec(`SET LOCAL lock_timeout = '` + lockTimeout + `'`).Error; err != nil {
			return err
		}
		return t.Exec(fmt.Sprintf(`COMMENT ON INDEX "event-management".%s IS '%s'`, tenantDeviceIndex.replaces,
			strings.ReplaceAll(note, "'", "''"))).Error
	})
}
