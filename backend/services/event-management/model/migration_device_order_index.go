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

// deviceOrderIndexSnapshot is this migration's own snapshot: the index it builds, exactly as
// pg_get_indexdef renders it, and the index it replaces. Nothing here reads the baseline, the
// live models or another migration.
type deviceOrderIndexSnapshot struct {
	table    string // in "event-management"
	name     string
	columns  string // as written in CREATE INDEX and as the catalog signature renders them
	def      string // pg_get_indexdef of a correct index
	replaces string
}

var deviceOrderIndex = deviceOrderIndexSnapshot{
	table:    "events",
	name:     "idx_events_tenant_device_order",
	columns:  "tenant_id, device_token, occurred_time DESC, event_id DESC",
	def:      `CREATE INDEX idx_events_tenant_device_order ON "event-management".events USING btree (tenant_id, device_token, occurred_time DESC, event_id DESC)`,
	replaces: "idx_events_tenant_device_time",
}

// deviceOrderIndexManualDrop and deviceOrderIndexManualBuild are the two statements every
// refusal hands the operator. The build is chunk by chunk (timescaledb.transaction_per_chunk),
// as for the per-device index it replaces: each chunk is indexed and committed on its own, so a
// write waits only while the chunk it targets is being indexed. It has no IF NOT EXISTS on
// purpose: an index of that name already there, including the INVALID one an interrupted build
// leaves, must fail the statement loudly rather than be skipped as though it were built. The
// DROP first removes exactly that leftover, and is a no-op when there is none.
const (
	deviceOrderIndexManualDrop  = `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_order`
	deviceOrderIndexManualBuild = `CREATE INDEX idx_events_tenant_device_order ` +
		`ON "event-management".events (tenant_id, device_token, occurred_time DESC, event_id DESC) ` +
		`WITH (timescaledb.transaction_per_chunk)`
)

// deviceOrderIndexManualAdvice is what every refusal says, ending without a full stop so a
// message can continue it. None of them asks for a recreate: the schema is correct without
// this index, only slower on a device's event list.
const deviceOrderIndexTooSlowMarker = "devicechain:device-order-index-too-slow"

const deviceOrderIndexManualAdvice = "To finish it in place, run these on the event store's primary, outside " +
	"event-management's startup and at a quiet time: " + deviceOrderIndexManualDrop + "; " +
	deviceOrderIndexManualBuild + ". The build goes chunk by chunk, so writes to a chunk wait only while that " +
	"chunk is indexed. If it is interrupted it leaves an invalid index of that name, which the DROP removes " +
	"before the next try. Then restart event-management, which removes the index it replaces. The same " +
	"statements can be run before upgrading"

const deviceOrderIndexTooSlowMessage = "building %s on \"event-management\".events did not finish within %s once " +
	"the table was locked, so it was rolled back and the table keeps %s. A build this slow would hold writes on " +
	"every start, so it is not retried: event-management now stops at once on every start until the index " +
	"exists. " + deviceOrderIndexManualAdvice + ". To let a start try once more instead, clear the marker with: " +
	"COMMENT ON INDEX \"event-management\".%s IS NULL. Error: %w"

const deviceOrderIndexRefusedMessage = "building %s on \"event-management\".events was refused: an earlier start " +
	"found it too slow and marked %s (%q). Nothing was locked or changed. " + deviceOrderIndexManualAdvice +
	". To let a start try once more instead, clear the marker with: COMMENT ON INDEX \"event-management\".%s IS NULL."

const deviceOrderIndexOutOfTimeMessage = "completing the per-device event index order stopped because this " +
	"start's budget of %s was spent. Nothing is half-applied, and this migration continues from here on the next start."

const deviceOrderIndexMarkerFailedNote = "%w (and the marker that would refuse the next start could not be written, " +
	"so every start will try the build again until the index exists: %v)"

// NewDeviceOrderIndexSchema makes the per-device event index carry the whole order of a
// device's event list. That list is ORDER BY occurred_time DESC, event_id DESC; the index was
// (tenant_id, device_token, occurred_time DESC), so every read of it needed an incremental sort
// on event_id above the index scan, and the planner could instead walk the primary key
// (tenant_id, occurred_time, event_id), which already gives the whole order, and check the
// device per row. Which one it picked was a statistics estimate. From TimescaleDB 2.30 an
// ordered LIMIT read of a hypertable goes through a DeferredChunkAppend node that, measured,
// only builds its per-chunk plans from paths that already produce the full order. A device's
// type-filtered page (an event_type predicate makes the planner expect almost no rows) then
// always walked the tenant's primary key and read every one of the tenant's rows to find a few
// of the device's: 100,004 rows discarded for 5 returned, 60 ms against 0.3 ms, and growing
// with the tenant's history. With event_id as the last key column the index gives the whole
// order itself, so every plan that wanted it is one index scan, on either TimescaleDB.
//
// The new index is (tenant_id, device_token, occurred_time DESC, event_id DESC). It serves
// everything the old one did (every read that used a prefix of it) and replaces it; the cost
// is the width of event_id in every entry of that one index. It is built under a NEW name
// before the old one goes, so a device read always has an index.
//
// Properties, shared with the other index migrations of this area:
//
//  1. Catalog first, no lock. Done (new valid, old gone) takes none. A same-named index that
//     is not the one built here ends the migration before any DDL: it does not replace an
//     index it did not write. An INVALID leftover of an interrupted build is dropped and rebuilt.
//  2. Gated before any lock on chunks and not-yet-compressed rows, with the key rebuild's
//     bounds; past either it changes nothing and hands the operator the build to run by hand.
//     It does NOT ask for a recreate: this is a performance index on a schema that is
//     otherwise correct.
//  3. The build is ONE transaction under LOCK ... IN SHARE MODE (writers wait, readers do not)
//     with SET LOCAL lock_timeout and statement_timeout; a busy table is retried until the
//     budget is spent, then the next start resumes. A build that runs out its whole
//     allowance once locked is NOT retried, in this start or any later one: it marks the old
//     index (as the tenant-led device index migration does), and every later start refuses
//     at once, without locking, until the index exists or the marker is cleared. Otherwise
//     each restart of a crash-looping pod would hold the store's writers for the allowance.
//  4. The old index is dropped in its own short bounded transaction, after the new one is
//     committed. Individually re-runnable at every step.
//
// There is no Rollback. A pod still running the previous release keeps working: its reads use
// the new index through a prefix, and nothing arbitrates on either index.
func NewDeviceOrderIndexSchema() *gormigrate.Migration {
	return newDeviceOrderIndexSchema(timeLeadingKeysDefaultTiming)
}

func newDeviceOrderIndexSchema(timing timeLeadingKeysTiming) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261020000000",
		Migrate: func(tx *gorm.DB) error {
			return completeDeviceIndexOrder(tx, timing)
		},
	}
}

type deviceOrderIndexState int

const (
	deviceOrderIndexDone    deviceOrderIndexState = iota // new valid, old absent
	deviceOrderIndexDropOld                              // new valid, old present
	deviceOrderIndexBuild                                // new absent
	deviceOrderIndexRebuild                              // new present, the right definition, INVALID: a leftover
)

func completeDeviceIndexOrder(db *gorm.DB, timing timeLeadingKeysTiming) error {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timing.budget)

	state, err := classifyDeviceOrderIndex(db)
	if err != nil {
		return err
	}
	if state == deviceOrderIndexBuild || state == deviceOrderIndexRebuild {
		if err := gateDeviceOrderIndex(db, timing); err != nil {
			return err
		}
		if state == deviceOrderIndexRebuild {
			// After the gates, so a refusal changes nothing. The leftover serves no read.
			if err := dropIndexBounded(db, trimmedIndex{deviceOrderIndex.name, deviceOrderIndex.table},
				dropTiming(timing), deadline); err != nil {
				return err
			}
		}
		if err := buildDeviceOrderIndexWithRetry(ctx, db, timing, deadline); err != nil {
			return err
		}
	}
	if err := dropIndexBounded(db, trimmedIndex{deviceOrderIndex.replaces, deviceOrderIndex.table},
		dropTiming(timing), deadline); err != nil {
		return fmt.Errorf("completing the per-device event index order: %s is built; removing %s failed: %w",
			deviceOrderIndex.name, deviceOrderIndex.replaces, err)
	}
	return nil
}

// gateDeviceOrderIndex refuses, changing nothing and locking nothing, a table with more chunks
// than maxChunks or more not-yet-compressed rows than maxRows.
func gateDeviceOrderIndex(db *gorm.DB, timing timeLeadingKeysTiming) error {
	tables := []string{deviceOrderIndex.table}
	counts, err := chunksPerTable(db, tables, timing.countTimeout)
	if err != nil {
		return fmt.Errorf("completing the per-device event index order: counting the chunks of events failed, "+
			"so nothing was changed; the next start tries again. If it fails the same way on every start, list "+
			"the chunks with: %s. Error: %w", chunkListQuery, err)
	}
	if over := overChunkCeiling(counts, tables, timing.maxChunks); over != "" {
		return fmt.Errorf("completing the per-device event index order: %s more than %d chunks, the most this "+
			"release builds an index over within one start, so this start changed nothing. %s. List the chunks "+
			"with: %s", over, timing.maxChunks, deviceOrderIndexManualAdvice, chunkListQuery)
	}
	rows, err := rowsToRekey(db, tables, timing.maxRows, timing.countTimeout)
	if err != nil {
		return fmt.Errorf("completing the per-device event index order: counting the rows to index failed, so "+
			"nothing was changed; the next start tries again. Error: %w", err)
	}
	if rows > timing.maxRows {
		return fmt.Errorf("completing the per-device event index order: more than %d rows of "+
			"\"event-management\".events not yet compressed would have to be indexed, longer than event-management "+
			"may spend starting, so this start changed nothing. %s", timing.maxRows, deviceOrderIndexManualAdvice)
	}
	return nil
}

// classifyDeviceOrderIndex reads the catalog only, and takes no lock on the table (the shape
// comes from pg_index and pg_attribute, never pg_get_indexdef, which opens the relation). The
// signature renders each key column with its DESC bit, as the definition does. A new index that
// is not the one this migration builds (columns, DESC bits, method, uniqueness, plainness) is
// refused. Null ordering, operator class and collation are not compared, the columns being
// NOT NULL.
func classifyDeviceOrderIndex(db *gorm.DB) (deviceOrderIndexState, error) {
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
		GROUP BY i.oid`, deviceOrderIndex.table, deviceOrderIndex.name).Scan(&found).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", deviceOrderIndex.name, err)
	}
	var old struct {
		Present bool
		Note    sql.NullString
	}
	if err := db.Raw(`SELECT to_regclass(format('%I.%I', 'event-management', ?::text)) IS NOT NULL AS present,
		obj_description(to_regclass(format('%I.%I', 'event-management', ?::text)), 'pg_class') AS note`,
		deviceOrderIndex.replaces, deviceOrderIndex.replaces).Scan(&old).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", deviceOrderIndex.replaces, err)
	}

	valid := false
	if len(found) > 0 {
		f := found[0]
		if f.Signature.String != deviceOrderIndex.columns || f.Method.String != "btree" || f.Unique.Bool ||
			!f.Plain.Bool || !f.OnEvents.Bool {
			return 0, fmt.Errorf("an index named %s exists on \"event-management\" as (%s%s%s), which is not the "+
				"index this migration builds (%s). Nothing was changed: this migration does not replace an index it "+
				"did not write. Drop or rename it, then restart event-management.",
				deviceOrderIndex.name, f.Signature.String, map[bool]string{true: ", unique", false: ""}[f.Unique.Bool],
				map[bool]string{true: "", false: " on another table"}[f.OnEvents.Bool], deviceOrderIndex.columns)
		}
		valid = f.Valid.Bool
	}
	switch {
	case valid && old.Present:
		return deviceOrderIndexDropOld, nil
	case valid:
		return deviceOrderIndexDone, nil
	}
	if old.Note.Valid && strings.HasPrefix(old.Note.String, deviceOrderIndexTooSlowMarker) {
		return 0, fmt.Errorf(deviceOrderIndexRefusedMessage, deviceOrderIndex.name, deviceOrderIndex.replaces,
			old.Note.String, deviceOrderIndex.replaces)
	}
	if len(found) > 0 {
		return deviceOrderIndexRebuild, nil
	}
	return deviceOrderIndexBuild, nil
}

// buildDeviceOrderIndexWithRetry builds the index, retrying a busy table until the deadline. It
// is the tenant-led device index's loop (buildDeviceIndexWithRetry) for this index: a build
// that ran out of time once locked is not a busy table, and is never retried.
func buildDeviceOrderIndexWithRetry(ctx context.Context, db *gorm.DB, timing timeLeadingKeysTiming,
	deadline time.Time) error {
	for {
		if time.Now().Add(timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf(deviceOrderIndexOutOfTimeMessage, timing.budget)
		}
		failed, err := buildDeviceOrderIndex(db, timing, deadline)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("build %s on \"event-management\".events: %w", deviceOrderIndex.name, ctx.Err())
		}
		final, tooSlow := deviceOrderIndexFailureError(failed, err, timing)
		if tooSlow {
			if markErr := markDeviceOrderIndexTooSlow(db, timing); markErr != nil {
				return fmt.Errorf(deviceOrderIndexMarkerFailedNote, final, markErr)
			}
		}
		if final != nil {
			return final
		}
		if time.Now().Add(timing.pause + timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf("could not lock \"event-management\".events to build %s: the table stayed busy "+
				"until this start's budget of %s was spent. Nothing was changed, and this migration continues from "+
				"here on the next start. If it fails the same way on every start: %s. Last error: %w",
				deviceOrderIndex.name, timing.budget, deviceOrderIndexManualAdvice, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("build %s on \"event-management\".events: %w", deviceOrderIndex.name, ctx.Err())
		case <-time.After(timing.pause):
		}
	}
}

// buildDeviceOrderIndex builds the index inside ONE transaction: lock events and every
// compressed relation IN SHARE MODE under a bounded wait, then create the index under what is
// left of the build allowance. It reports, on failure, what the attempt got to.
func buildDeviceOrderIndex(db *gorm.DB, timing timeLeadingKeysTiming, deadline time.Time) (rekeyAttempt, error) {
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
		compressed, err := compressedRelations(t, deviceOrderIndex.table)
		if err != nil {
			return err
		}
		targets := append([]string{`"event-management".` + deviceOrderIndex.table}, compressed...)
		if err := t.Exec(`LOCK TABLE ` + strings.Join(targets, ", ") + ` IN SHARE MODE`).Error; err != nil {
			return err
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
			deviceOrderIndex.name, deviceOrderIndex.table, deviceOrderIndex.columns)).Error; err != nil {
			// statement_timeout bounds each statement; it starts after this clock, so an
			// elapsed time under the allowance cannot be its timeout.
			attempt.ranOut = time.Since(began) >= allowance
			return err
		}
		return nil
	})
	return attempt, err
}

// deviceOrderIndexFailureError decides what one failed attempt means: nil for a busy table,
// which the caller retries, or the error that ends the migration, and whether that error is
// the too-slow verdict the caller makes stick. It is deviceIndexFailureError's table:
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
func deviceOrderIndexFailureError(attempt rekeyAttempt, err error, timing timeLeadingKeysTiming) (final error, tooSlow bool) {
	var pgErr *pgconn.PgError
	isPg := errors.As(err, &pgErr)
	name := deviceOrderIndex.name
	switch {
	case isLockTableFull(err):
		return fmt.Errorf("building %s on \"event-management\".events: the server ran out of lock slots (the "+
			"build locks the table and every chunk of it). Raise max_locks_per_transaction on the event store and "+
			"restart; nothing was changed. Error: %w", name, err), false
	case errors.Is(err, errRekeyOutOfTime):
		return fmt.Errorf(deviceOrderIndexOutOfTimeMessage, timing.budget), false
	case isPg && (pgErr.Code == "55P03" || pgErr.Code == "40P01"):
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.phase == rekeyLocking:
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.ranOut && attempt.full:
		return fmt.Errorf(deviceOrderIndexTooSlowMessage, name, timing.tableBuild, deviceOrderIndex.replaces,
			deviceOrderIndex.replaces, err), true
	case isPg && pgErr.Code == "57014" && attempt.ranOut:
		return fmt.Errorf(deviceOrderIndexOutOfTimeMessage, timing.budget), false
	default:
		return fmt.Errorf("building %s on \"event-management\".events failed and was rolled back; the next "+
			"start tries again. If it fails the same way on every start: %s. Error: %w",
			name, deviceOrderIndexManualAdvice, err), false
	}
}

// markDeviceOrderIndexTooSlow leaves the too-slow marker on the OLD index, in a short
// transaction of its own (the build's was rolled back). COMMENT takes SHARE UPDATE EXCLUSIVE
// on the index, which ingest's inserts do not conflict with; the lock_timeout only guards
// against another DDL holding it. If the old index is gone the COMMENT fails and the caller
// says so.
func markDeviceOrderIndexTooSlow(db *gorm.DB, timing timeLeadingKeysTiming) error {
	note := fmt.Sprintf("%s: building %s did not finish within %s on %s; event-management refuses to retry it. "+
		"Clear this comment to try again.", deviceOrderIndexTooSlowMarker, deviceOrderIndex.name,
		timing.tableBuild, time.Now().UTC().Format(time.RFC3339))
	lockTimeout, err := timeoutSetting(timing.lockTimeout)
	if err != nil {
		return err
	}
	return db.Transaction(func(t *gorm.DB) error {
		if err := t.Exec(`SET LOCAL lock_timeout = '` + lockTimeout + `'`).Error; err != nil {
			return err
		}
		return t.Exec(fmt.Sprintf(`COMMENT ON INDEX "event-management".%s IS '%s'`, deviceOrderIndex.replaces,
			strings.ReplaceAll(note, "'", "''"))).Error
	})
}
