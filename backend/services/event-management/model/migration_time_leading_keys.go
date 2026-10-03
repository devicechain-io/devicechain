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

// timeLeadingKey is this migration's own snapshot of one identity key it rebuilds: the
// table, the key's name (kept across the swap), the column lists before and after, as
// golden/event-management.sql recorded them, and the (tenant_id, occurred_time DESC)
// index the new key's prefix makes redundant. A snapshot, not a reference: nothing here
// reads the live models, the baseline, or the conflict lists in api.go.
type timeLeadingKey struct {
	table      string // in "event-management"
	name       string // the index name; for events, the PRIMARY KEY constraint and its index
	primaryKey bool   // events_pkey is a constraint; the others are unique indexes
	oldColumns string // comma-joined, no spaces: what pg_index reports before
	newColumns string // comma-joined, no spaces: what pg_index reports after
	dropped    string // "" for event_anchors, which has no tenant-time index
}

// timeLeadingKeys lists the five identity keys that led with a digest (event_id or
// payload_id, a SHA-256), so every insert landed on a random leaf of the index and, after
// each checkpoint, wrote a full-page image of it. Each is rebuilt to lead with
// (tenant_id, occurred_time): consecutive inserts then land on the same rightmost leaf.
//
// The column SET of every key is unchanged, only its order. That is load-bearing: ON
// CONFLICT infers its arbiter from the set, so the writers' conflict lists need no change,
// and a pod still running the previous release keeps writing for the whole rolling upgrade.
//
// With time second, each new key also serves every read the (tenant_id, occurred_time
// DESC) index served: every application read carries tenant_id = ? (the fail-closed tenant
// callback), and the newest-first order is the key read backward, event_id or payload_id
// included as the DefaultOrder tiebreak. So that index is dropped on the four tables that
// had one. Kept, deliberately: events_device_token_occurred_time_idx (the per-device read),
// measurement_events_occurred_time_idx (the measurement_rollups refresh's cross-tenant time
// range), idx_events_tenant_alt_id, idx_event_anchors_lookup,
// idx_measurement_tenant_device_name_time, and all of state_change_events, whose key
// already puts time after the device.
//
// Events go first: the largest and hottest table, where the whole migration's budget is
// most useful.
var timeLeadingKeys = []timeLeadingKey{
	{"events", "events_pkey", true,
		"tenant_id,event_id,occurred_time", "tenant_id,occurred_time,event_id",
		"events_tenant_id_occurred_time_idx"},
	{"measurement_events", "uq_measurement_events_idem", false,
		"tenant_id,payload_id,occurred_time", "tenant_id,occurred_time,payload_id",
		"measurement_events_tenant_id_occurred_time_idx"},
	{"location_events", "uq_location_events_idem", false,
		"tenant_id,payload_id,occurred_time", "tenant_id,occurred_time,payload_id",
		"location_events_tenant_id_occurred_time_idx"},
	{"alert_events", "uq_alert_events_idem", false,
		"tenant_id,payload_id,occurred_time", "tenant_id,occurred_time,payload_id",
		"alert_events_tenant_id_occurred_time_idx"},
	{"event_anchors", "uq_event_anchors_idem", false,
		"tenant_id,event_id,occurred_time,anchor_type,anchor_token",
		"tenant_id,occurred_time,event_id,anchor_type,anchor_token", ""},
}

// timeLeadingKeysTiming bounds the migration. Unlike the index trim it BUILDS, and a build
// over live chunks holds the table against ingest for as long as it runs, so every phase
// has its own bound.
type timeLeadingKeysTiming struct {
	// lockTimeout bounds one lock wait (SET LOCAL lock_timeout).
	lockTimeout time.Duration
	// lockAttempt bounds one attempt at locking a table, its chunks and their compressed
	// relations (SET LOCAL statement_timeout on the one LOCK statement).
	lockAttempt time.Duration
	// pause is the gap between lock attempts, in which the table's writers run freely.
	pause time.Duration
	// tableBuild bounds one table's swap once it is locked (SET LOCAL statement_timeout).
	// Only a swap that ran out of this WHOLE allowance is "too slow".
	tableBuild time.Duration
	// minBuild is the least time a swap is ever started with. statement_timeout = 0 turns
	// the timeout OFF, so a remainder that would round to it must never be SET.
	minBuild time.Duration
	// budget bounds the whole migration in one start: the history count, every lock
	// attempt and every swap. A swap is given min(tableBuild, what is left of it).
	budget time.Duration
	// countTimeout bounds the history count (SET LOCAL statement_timeout).
	countTimeout time.Duration
	// maxRows is the most rows the rebuild may have to index: the rows not yet moved into
	// a compressed chunk's columnar store, over every table whose key is still the old one.
	maxRows int64
	// maxChunks is the most chunks a table this start would lock may have
	// (eventStoreMaxChunks). Zero is not "unlimited": it refuses any table with a chunk.
	maxChunks int64
	// buildMemory is SET LOCAL maintenance_work_mem for the swap's index sorts.
	buildMemory string
}

// timeLeadingKeysDefaultTiming. The startup probe gives event-management 150 s
// (deploy/helm values: 5 s × 30), and migrations run before /readyz exists.
//
//   - maxRows = 4,000,000. The slowest rate measured for these builds is 3.1 µs a row
//     (58.3 s for 18.8 M rows of measurement_events under 6,000 events/s of live ingest), so
//     4 M rows is about 12 s, against a 40 s tableBuild. buildMemory = 256MB, because the
//     event store's own maintenance_work_mem is 64MB and the sort is about 20% slower there.
//   - Chunks cost too, compressed or not, and the row count does not see them: on the
//     pinned image, rebuilding events_pkey on a table of 1,000 daily chunks (950 of them
//     compressed, nearly three years at the default chunk interval with retention off, the
//     default) took 10.8 s, and a unique index on the same shape 0.85 s (up to 28.2 s for
//     events_pkey on slower development workstations; see eventStoreMaxChunks). maxChunks =
//     eventStoreMaxChunks, half that shape, holds every table to where tableBuild covers the
//     rebuild with room on slower storage: a table with more chunks is refused before anything
//     is locked, and marks nothing. One within it whose build still runs out is reported too
//     slow (below).
//   - budget = 60 s for everything in one start. On an upgrade from v0.18.0 the index trim
//     runs in the same start first; its own budget is 60 s and it takes well under a second
//     uncontended. Expected total: seconds. Worst case: the trim's budget, this budget and
//     the rest of startup can exceed the probe, in which case the pod is restarted. That is
//     a restart, not a stall: neither migration leaves a table half-done (each table's
//     swap is one transaction, rolled back on any error, and server-side the swap is
//     bounded by its own statement_timeout even if the pod is gone), the trim is then
//     recorded as applied, and the next start continues from the first table not yet
//     re-keyed with the whole budget.
//
// What the previous pod sees: during one table's swap its reads and writes of that table
// wait, for at most lockAttempt + tableBuild (45 s). A persist transaction is then active,
// not idle, so IdleInTransactionTimeout does not end it; a tenant erasure that is waiting
// for writers to settle (five minutes by default) waits that much longer. Persist has no
// deadline and the broker's AckWait is 60 s, so a held batch is at worst redelivered, and
// redelivery is idempotent. Events arriving meanwhile wait unread in the resolved-events
// stream: at 6,000 events/s a 45 s pause is 270,000 of them, which stays under 90% of the
// default 1 GiB per hot stream (where ingest is refused) unless an event averages more
// than about 3.5 KB on the stream.
var timeLeadingKeysDefaultTiming = timeLeadingKeysTiming{
	lockTimeout:  3 * time.Second,
	lockAttempt:  5 * time.Second,
	pause:        2 * time.Second,
	tableBuild:   40 * time.Second,
	minBuild:     time.Second,
	budget:       60 * time.Second,
	countTimeout: 10 * time.Second,
	maxRows:      4_000_000,
	maxChunks:    eventStoreMaxChunks,
	buildMemory:  "256MB",
}

// timeLeadingKeysRefusedMarker begins the comment a too-slow swap leaves on the key's
// index, so the next start refuses at once, without locking anything. COMMENT takes a
// SHARE UPDATE EXCLUSIVE lock on the index, which does not conflict with ingest's inserts.
const timeLeadingKeysRefusedMarker = "devicechain:rekey-too-slow"

// recreateAdvice is what every message that names a recreate says about it: what it
// costs, and what to do first. Recreating discards the WHOLE instance, not only its events.
const recreateAdvice = "recreate the instance with `dcctl destroy` and `dcctl bootstrap`. " +
	"Recreating discards ALL of the instance's data — tenants, devices and their definitions, " +
	"dashboards, users and event history — so export what you need first"

const timeLeadingKeysHistoryMessage = "rebuilding the event store's identity keys: more than %d rows that are " +
	"not yet compressed would have to be indexed (tables %s). That would take longer than event-management " +
	"may spend starting, so this start changed nothing (%s). This pre-release upgrade cannot carry this " +
	"instance forward in place: " + recreateAdvice + ". The previous event-management keeps storing events until then."

const timeLeadingKeysTooSlowMessage = "rebuilding the key %s of \"event-management\".%s did not finish within %s " +
	"once the table was locked, so it was rolled back and the table keeps its previous key (%s). A rebuild this " +
	"slow would hold the table against ingest on every start, so it is not retried: event-management now stops " +
	"at once on every start, without locking anything, until the instance is recreated. To carry this instance " +
	"forward, " + recreateAdvice + ". To try once more instead (for example after moving the event store to " +
	"faster storage), clear the marker with: COMMENT ON INDEX \"event-management\".%s IS NULL. " +
	tooSlowChunksAdvice + " Error: %w"

const timeLeadingKeysRefusedMessage = "rebuilding the key %s of \"event-management\".%s was refused: an earlier start " +
	"found it too slow and marked it (%q). Nothing was locked or changed (%s). To carry this instance forward, " +
	recreateAdvice + ". To try once more instead, clear the marker with: COMMENT ON INDEX \"event-management\".%s IS NULL. " +
	tooSlowChunksAdvice

// tooSlowChunksAdvice is what both too-slow messages say about chunks: a rebuild's time grows
// with the table's chunks, and the in-place way to have fewer is drop_chunks. chunkListQuery
// carries no '%', so it is safe inside a format string.
const tooSlowChunksAdvice = "If the table holds chunks of events dated far in the past (list them with: " +
	chunkListQuery + "), removing the ones you do not need with the batched drop_chunks procedure in this " +
	"release's upgrade notes shortens the rebuild; that deletes every tenant's events in them for good. " +
	"Then clear the marker."

const timeLeadingKeysBusyMessage = "could not lock \"event-management\".%s to rebuild its key %s: the table stayed busy " +
	"on every attempt (%d attempts, each bounded to %s, within %s). Nothing is half-applied (%s), and this " +
	"migration continues from here on the next start. The previous event-management keeps storing events " +
	"meanwhile. Find the transaction holding the table with: %s; last error: %w"

const timeLeadingKeysOutOfTimeMessage = "rebuilding the event store's identity keys stopped before finishing " +
	"\"event-management\".%s, because this start's %s was spent. Nothing is half-applied (%s), and this " +
	"migration continues from here on the next start. The previous event-management keeps storing events meanwhile."

const timeLeadingKeysLockTableFullMessage = "rebuilding the key %s of \"event-management\".%s: the server ran out of " +
	"lock slots (the rebuild locks the table and every chunk of it). Raise max_locks_per_transaction on the event " +
	"store and restart; the table keeps its previous key (%s), and this migration continues from here. Error: %w"

const timeLeadingKeysFailedMessage = "rebuilding the key %s of \"event-management\".%s failed and was rolled back: the " +
	"table keeps its previous key (%s). The next start tries again. If it fails the same way on every start, this " +
	"instance cannot be re-keyed in place: " + recreateAdvice + ". Error: %w"

const timeLeadingKeysShapeMessage = "the key %s of \"event-management\".%s is (%s)%s, which is neither its previous " +
	"shape (%s) nor its new one (%s), unique and valid. Nothing was changed (%s): this migration rebuilds only a key " +
	"it recognises. Restore the key, or " + recreateAdvice + "."

// errRekeyOutOfTime ends a table's attempt that could not be given minBuild to swap in.
var errRekeyOutOfTime = errors.New("the migration's time budget is spent")

type rekeyPhase int

const (
	rekeyLocking rekeyPhase = iota
	rekeyBuilding
)

// rekeyAction is what one key needs, read from the catalog before any DDL.
type rekeyAction int

const (
	rekeyDone rekeyAction = iota
	rekeySwap
	rekeyDropOnly
)

type rekeyPlan struct {
	key    timeLeadingKey
	action rekeyAction
}

// NewTimeLeadingKeysSchema rebuilds the event store's five identity keys to lead with
// (tenant_id, occurred_time), under their existing names, and drops the four
// (tenant_id, occurred_time DESC) indexes their prefix makes redundant (see
// timeLeadingKeys).
//
// It BUILDS, which the trim deliberately did not: a swap holds the table ACCESS EXCLUSIVE
// for as long as the build runs. The properties below are load-bearing:
//
//  1. **It reads the catalog first and changes nothing it does not recognise.** Each key
//     must be exactly its old or its new shape (columns in order, unique, valid, no
//     predicate, primary only for events_pkey); anything else ends the migration before
//     any DDL. That reading is also what makes it re-runnable: a key already rebuilt is
//     skipped with no lock taken.
//  2. **Before any lock it counts each table's chunks, from the catalog**, and refuses a
//     table it would lock that has more than maxChunks, changing and marking nothing, so
//     the next start counts again (checkChunkCeiling). Then, **before any DDL, it counts, up
//     to maxRows + 1, the rows the rebuild would index**:
//     the heap rows of every chunk of every table still on its old key (ONLY the chunk, so
//     rows already moved into a compressed chunk's columnar store, which the build does
//     not index, are not counted; rows inserted into a compressed chunk's range live in
//     its heap and are). Past maxRows it stops, having changed nothing, with the recreate
//     remedy: no startup window holds that build.
//  3. **Each table is one transaction**: lock the table, its chunks and their compressed
//     relations under a bounded wait, then swap the key under its own name and drop the
//     redundant index under statement_timeout. Any failure rolls the whole table back, so
//     a table always has exactly one key, and other sessions never see it without its
//     conflict arbiter: they queue behind the lock until COMMIT, when the new key exists.
//     events_pkey is swapped in ONE ALTER TABLE (DROP CONSTRAINT, ADD CONSTRAINT): a
//     hypertable refuses ADD CONSTRAINT ... USING INDEX, so the key cannot be built first
//     and attached after. Tables already done stay done if a later one fails, and the next
//     start continues from the first one not done.
//  4. **Locking first is what gives a timeout one meaning.** LOCK TABLE without ONLY takes
//     every chunk; the compressed relations are not children of the table, but the swap
//     touches them (measured: two locks per compressed chunk), so they are locked in the
//     same statement. A failure while locking is a busy table: retried every pause within
//     the budget, then a resumable error naming the holder query. Once locked, a 57014
//     means the build itself was slow, or someone cancelled it (pg_cancel_backend), and
//     only a statement that ran for its whole statement_timeout is taken as the first.
//  5. **"Too slow" is decided once, and sticks.** A swap that was given its whole
//     tableBuild allowance and ran out of it (a cancel is not a verdict) leaves a marker
//     comment on the key's index and ends the migration with the recreate remedy; a
//     cancelled swap is retried on the next start. The next start reads the marker and
//     refuses at once, without locking anything, rather than stall ingest for another
//     40 s on every restart. A swap that timed out with LESS than tableBuild (because
//     earlier tables spent the budget) is not a verdict: the next start gives it the whole
//     allowance.
//  6. **Every SET is SET LOCAL inside db.Transaction**, so nothing outlives the
//     transaction on a pooled connection; SET LOCAL outside a transaction is a no-op,
//     which is why the wrapper is load-bearing although migrations run with
//     UseTransaction:false. statement_timeout is never set to less than minBuild:
//     a value that rounds to 0 turns the timeout OFF.
//
// There is deliberately no Rollback, as for the trim: rebuilding the old keys is the same
// build again, and gormigrate reports ErrRollbackImpossible for a nil Rollback. The
// previous release works on the new keys, because its conflict lists name the same sets.
func NewTimeLeadingKeysSchema() *gormigrate.Migration {
	return newTimeLeadingKeysSchema(timeLeadingKeysDefaultTiming)
}

func newTimeLeadingKeysSchema(timing timeLeadingKeysTiming) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261001000000",
		Migrate: func(tx *gorm.DB) error {
			return rekeyIdentityKeys(tx, timing)
		},
	}
}

func rekeyIdentityKeys(db *gorm.DB, timing timeLeadingKeysTiming) error {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timing.budget)

	// 1. Read every key's state; no DDL until every key is recognised.
	var plans []rekeyPlan
	for _, k := range timeLeadingKeys {
		action, err := classifyKey(db, k)
		if err != nil {
			return err
		}
		plans = append(plans, rekeyPlan{k, action})
	}

	// 2. The chunk gate, over every table this start would lock (a drop-only table is locked
	// whole too), then the history gate, over the tables that build. The chunk count comes
	// first, and the order is load-bearing: it is one catalog read, while the row count is a
	// statement per chunk, so on a table with too many chunks the row count is itself what
	// would run out of time. Both come before step 3's first LOCK.
	var locking, building []string
	for _, p := range plans {
		if p.action != rekeyDone {
			locking = append(locking, p.key.table)
		}
		if p.action == rekeySwap {
			building = append(building, p.key.table)
		}
	}
	if err := checkChunkCeiling(db, "rebuilding the event store's identity keys", rekeyProgress(plans),
		locking, timing.maxChunks, timing.countTimeout); err != nil {
		return err
	}
	if len(building) > 0 {
		rows, err := rowsToRekey(db, building, timing.maxRows, timing.countTimeout)
		if err != nil {
			return fmt.Errorf("rebuilding the event store's identity keys: counting the rows to index failed, "+
				"so nothing was changed (%s); the next start tries again: %w", rekeyProgress(plans), err)
		}
		if rows > timing.maxRows {
			return fmt.Errorf(timeLeadingKeysHistoryMessage, timing.maxRows, strings.Join(building, ", "),
				rekeyProgress(plans))
		}
	}

	// 3. One table at a time.
	for i := range plans {
		p := &plans[i]
		if p.action == rekeyDone {
			continue
		}
		if err := rekeyWithRetry(ctx, db, plans, i, timing, deadline); err != nil {
			return err
		}
		p.action = rekeyDone
	}
	return nil
}

// classifyKey reads one key's shape from the catalog and decides what it needs. A key
// carrying the too-slow marker is refused here, before any lock.
func classifyKey(db *gorm.DB, k timeLeadingKey) (rekeyAction, error) {
	var st struct {
		Cols     sql.NullString
		Primary  sql.NullBool
		Unique   sql.NullBool
		Valid    sql.NullBool
		NoPred   sql.NullBool
		Note     sql.NullString
		OnItsOwn sql.NullBool
	}
	err := db.Raw(`SELECT string_agg(a.attname, ',' ORDER BY k.ord) AS cols,
		bool_and(x.indisprimary) AS "primary", bool_and(x.indisunique) AS "unique",
		bool_and(x.indisvalid) AS valid, bool_and(x.indpred IS NULL) AS no_pred,
		max(obj_description(i.oid, 'pg_class')) AS note, bool_and(t.relname = ?) AS on_its_own
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = i.relnamespace
		CROSS JOIN LATERAL unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
		WHERE n.nspname = 'event-management' AND i.relname = ?`, k.table, k.name).Scan(&st).Error
	if err != nil {
		return 0, fmt.Errorf("read the key %s of \"event-management\".%s: %w", k.name, k.table, err)
	}
	if st.Note.Valid && strings.HasPrefix(st.Note.String, timeLeadingKeysRefusedMarker) {
		return 0, fmt.Errorf(timeLeadingKeysRefusedMessage, k.name, k.table, st.Note.String,
			"no table was changed by this start", k.name)
	}
	recognised := st.Cols.Valid && st.Primary.Bool == k.primaryKey && st.Unique.Bool &&
		st.Valid.Bool && st.NoPred.Bool && st.OnItsOwn.Bool
	dropPresent := false
	if k.dropped != "" {
		if err := db.Raw(`SELECT to_regclass(format('%I.%I', 'event-management', ?::text)) IS NOT NULL`,
			k.dropped).Scan(&dropPresent).Error; err != nil {
			return 0, fmt.Errorf("look for %s on \"event-management\".%s: %w", k.dropped, k.table, err)
		}
	}
	switch {
	case recognised && st.Cols.String == k.newColumns && !dropPresent:
		return rekeyDone, nil
	case recognised && st.Cols.String == k.newColumns:
		return rekeyDropOnly, nil
	case recognised && st.Cols.String == k.oldColumns:
		return rekeySwap, nil
	}
	found := "absent"
	if st.Cols.Valid {
		found = st.Cols.String
	}
	flags := fmt.Sprintf(", primary=%t unique=%t valid=%t partial=%t, on table %t",
		st.Primary.Bool, st.Unique.Bool, st.Valid.Bool, !st.NoPred.Bool, st.OnItsOwn.Bool)
	return 0, fmt.Errorf(timeLeadingKeysShapeMessage, k.name, k.table, found, flags,
		k.oldColumns, k.newColumns, "no table was changed by this start")
}

// rowsToRekey counts, up to limit+1, the rows a rebuild of tables' keys would index: the
// heap rows of every chunk (ONLY the chunk: rows already moved into a compressed chunk's
// columnar store are not indexed by the build), newest chunks first, stopping once past
// limit. The count runs under its own statement_timeout, so a cold or busy disk cannot
// turn it into a stall of its own.
func rowsToRekey(db *gorm.DB, tables []string, limit int64, timeout time.Duration) (int64, error) {
	var total int64
	err := db.Transaction(func(t *gorm.DB) error {
		setting, err := timeoutSetting(timeout)
		if err != nil {
			return err
		}
		if err := t.Exec(`SET LOCAL statement_timeout = '` + setting + `'`).Error; err != nil {
			return err
		}
		for _, table := range tables {
			var chunks []string
			if err := t.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
				FROM timescaledb_information.chunks
				WHERE hypertable_schema = 'event-management' AND hypertable_name = ?
				ORDER BY range_end DESC`, table).Scan(&chunks).Error; err != nil {
				return err
			}
			for _, chunk := range chunks {
				var n int64
				if err := t.Raw(`SELECT count(*) FROM (SELECT 1 FROM ONLY `+chunk+` LIMIT ?) s`,
					limit-total+1).Scan(&n).Error; err != nil {
					return err
				}
				total += n
				if total > limit {
					return nil
				}
			}
		}
		return nil
	})
	return total, err
}

// timeoutSetting renders d as a statement_timeout value, rounded UP to whole milliseconds.
// It refuses anything under one millisecond: PostgreSQL reads 0 as "no timeout", so a
// remainder that rounds to it would turn the bound off, and a negative value is an error
// the server would report as something else entirely.
func timeoutSetting(d time.Duration) (string, error) {
	if d < time.Millisecond {
		return "", fmt.Errorf("%w: %s left, under the one millisecond a timeout needs", errRekeyOutOfTime, d)
	}
	ms := (d + time.Millisecond - 1) / time.Millisecond
	return fmt.Sprintf("%dms", ms), nil
}

// buildAllowance is the time a swap is given: tableBuild, or what is left of the budget if
// that is less. full reports whether it is the whole tableBuild — only a swap that timed
// out with the whole allowance is too slow. Less than minBuild left is errRekeyOutOfTime:
// a swap is never started on a remainder that could round its statement_timeout to 0.
func buildAllowance(timing timeLeadingKeysTiming, deadline, now time.Time) (allowance time.Duration, full bool, err error) {
	left := deadline.Sub(now)
	if left >= timing.tableBuild {
		return timing.tableBuild, true, nil
	}
	if left < timing.minBuild {
		return 0, false, fmt.Errorf("%w: %s left for the swap, under the %s it is ever started with",
			errRekeyOutOfTime, left, timing.minBuild)
	}
	return left, false, nil
}

// rekeyAttempt is what one failed attempt at a table reports about itself.
type rekeyAttempt struct {
	// phase is where it failed: locking, or building once locked.
	phase rekeyPhase
	// full: the swap was given the whole tableBuild, not a remainder of the budget.
	full bool
	// ranOut: the statement that failed had run for at least its whole statement_timeout.
	// A 57014 is also what pg_cancel_backend raises, and an operator cancelling a long
	// rebuild has not shown that the store is too slow for it.
	ranOut bool
}

// rekeyWithRetry re-keys plans[i], retrying a busy table until the deadline.
func rekeyWithRetry(ctx context.Context, db *gorm.DB, plans []rekeyPlan, i int,
	timing timeLeadingKeysTiming, deadline time.Time) error {
	k := plans[i].key
	for attempt := 1; ; attempt++ {
		// Checked before EVERY attempt, the first included: an attempt is started only if
		// its lock wait and the least swap it may run both fit what is left.
		if time.Now().Add(timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf(timeLeadingKeysOutOfTimeMessage, k.table, "budget of "+timing.budget.String(),
				rekeyProgress(plans))
		}
		failed, err := rekeyTable(db, plans[i], timing, deadline)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("rebuild the key %s of \"event-management\".%s: %w", k.name, k.table, ctx.Err())
		}
		final, tooSlow := timeLeadingKeysFailureError(k, failed, err, timing, rekeyProgress(plans))
		if tooSlow {
			if markErr := markTooSlow(db, k, timing); markErr != nil {
				return fmt.Errorf("%w (and the marker that would refuse the next start could not be written: %v)",
					final, markErr)
			}
		}
		if final != nil {
			return final
		}
		if time.Now().Add(timing.pause + timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf(timeLeadingKeysBusyMessage, k.table, k.name, attempt, timing.lockAttempt,
				timing.budget, rekeyProgress(plans), indexTrimHolderQuery(k.table), err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("rebuild the key %s of \"event-management\".%s: %w", k.name, k.table, ctx.Err())
		case <-time.After(timing.pause):
		}
	}
}

// rekeyTable re-keys one table inside ONE transaction: lock the table, every chunk and
// their compressed relations, then swap the key under its own name and drop the index its
// prefix makes redundant. It reports, on failure, what the attempt got to (rekeyAttempt).
func rekeyTable(db *gorm.DB, p rekeyPlan, timing timeLeadingKeysTiming, deadline time.Time) (rekeyAttempt, error) {
	k := p.key
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
		// The compressed relations of this table's compressed chunks, read before the
		// lock and locked in the SAME statement as the table, so one lockAttempt bounds the
		// whole wait. A chunk compressed between this read and the lock is not covered; the
		// swap's own wait on it then ends at lock_timeout (55P03), which is retried.
		var compressed []string
		if err := t.Raw(`SELECT format('%I.%I', cc.schema_name, cc.table_name)
			FROM _timescaledb_catalog.chunk c
			JOIN _timescaledb_catalog.chunk cc ON cc.id = c.compressed_chunk_id
			JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
			WHERE h.schema_name = 'event-management' AND h.table_name = ?`, k.table).Scan(&compressed).Error; err != nil {
			return err
		}
		// Without ONLY, LOCK TABLE takes every inheritance child: every chunk.
		targets := append([]string{`"event-management".` + k.table}, compressed...)
		if err := t.Exec(`LOCK TABLE ` + strings.Join(targets, ", ") + ` IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
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
		var stmts []string
		if p.action == rekeySwap {
			cols := strings.ReplaceAll(k.newColumns, ",", ", ")
			if k.primaryKey {
				stmts = append(stmts, fmt.Sprintf(`ALTER TABLE "event-management".%s DROP CONSTRAINT %s, `+
					`ADD CONSTRAINT %s PRIMARY KEY (%s)`, k.table, k.name, k.name, cols))
			} else {
				stmts = append(stmts,
					fmt.Sprintf(`DROP INDEX "event-management".%s`, k.name),
					fmt.Sprintf(`CREATE UNIQUE INDEX %s ON "event-management".%s (%s)`, k.name, k.table, cols))
			}
		}
		if k.dropped != "" {
			stmts = append(stmts, fmt.Sprintf(`DROP INDEX IF EXISTS "event-management".%s`, k.dropped))
		}
		for _, s := range stmts {
			// statement_timeout bounds each statement; the server's clock starts after
			// this one, so an elapsed time here under the allowance cannot be its timeout.
			began := time.Now()
			if err := t.Exec(s).Error; err != nil {
				attempt.ranOut = time.Since(began) >= allowance
				return err
			}
		}
		return nil
	})
	return attempt, err
}

// timeLeadingKeysFailureError decides what one failed attempt means: nil for a busy table,
// which the caller retries, or the error that ends the migration, and whether that error
// is the too-slow verdict the caller makes stick. TestTimeLeadingKeysFailureError pins the
// message for each case.
//
//	phase     SQLSTATE                 outcome
//	any       53200 (lock table full)  stop; names max_locks_per_transaction
//	locking   55P03, 40P01, 57014      retry: another session holds the table, a chunk or a compressed relation
//	building  55P03, 40P01             retry: an internal relation was busy
//	building  57014, ran out, whole    stop for good: too slow (sticky, names the recreate)
//	building  57014, ran out, less     stop; resumes: this start's budget was spent
//	building  57014, did not run out   a cancel (pg_cancel_backend): as anything else, below
//	any       errRekeyOutOfTime        stop; resumes
//	any       anything else            stop; retried next start, names the recreate if it repeats
func timeLeadingKeysFailureError(k timeLeadingKey, attempt rekeyAttempt, err error,
	timing timeLeadingKeysTiming, progress string) (final error, tooSlow bool) {
	var pgErr *pgconn.PgError
	isPg := errors.As(err, &pgErr)
	switch {
	case isLockTableFull(err):
		return fmt.Errorf(timeLeadingKeysLockTableFullMessage, k.name, k.table, progress, err), false
	case errors.Is(err, errRekeyOutOfTime):
		return fmt.Errorf(timeLeadingKeysOutOfTimeMessage, k.table, "budget of "+timing.budget.String(), progress), false
	case isPg && (pgErr.Code == "55P03" || pgErr.Code == "40P01"):
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.phase == rekeyLocking:
		return nil, false
	case isPg && pgErr.Code == "57014" && attempt.ranOut && attempt.full:
		return fmt.Errorf(timeLeadingKeysTooSlowMessage, k.name, k.table, timing.tableBuild, progress, k.name, err), true
	case isPg && pgErr.Code == "57014" && attempt.ranOut:
		return fmt.Errorf(timeLeadingKeysOutOfTimeMessage, k.table, "budget of "+timing.budget.String(), progress), false
	default:
		return fmt.Errorf(timeLeadingKeysFailedMessage, k.name, k.table, progress, err), false
	}
}

// markTooSlow leaves the too-slow marker on the key's index, in a short transaction of its
// own (the swap's was rolled back). COMMENT takes SHARE UPDATE EXCLUSIVE on the index,
// which ingest's inserts do not conflict with; the lock_timeout only guards against
// another DDL holding it.
func markTooSlow(db *gorm.DB, k timeLeadingKey, timing timeLeadingKeysTiming) error {
	note := fmt.Sprintf("%s: rebuilding this key to lead with time did not finish within %s on %s; "+
		"event-management refuses to retry it. Clear this comment to try again.",
		timeLeadingKeysRefusedMarker, timing.tableBuild, time.Now().UTC().Format(time.RFC3339))
	lockTimeout, err := timeoutSetting(timing.lockTimeout)
	if err != nil {
		return err
	}
	return db.Transaction(func(t *gorm.DB) error {
		if err := t.Exec(`SET LOCAL lock_timeout = '` + lockTimeout + `'`).Error; err != nil {
			return err
		}
		return t.Exec(fmt.Sprintf(`COMMENT ON INDEX "event-management".%s IS '%s'`, k.name,
			strings.ReplaceAll(note, "'", "''"))).Error
	})
}

// rekeyProgress says, for an operator reading a failure, which tables are already on their
// new key, whether in this start or an earlier one.
func rekeyProgress(plans []rekeyPlan) string {
	var done []string
	for _, p := range plans {
		if p.action == rekeyDone {
			done = append(done, p.key.table)
		}
	}
	if len(done) == 0 {
		return "no table has been re-keyed yet"
	}
	return "already re-keyed: " + strings.Join(done, ", ")
}
