// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// deviceAltIdKeySnapshot is this migration's own snapshot: the unique index it builds and the
// one it replaces. Nothing here reads the baseline, the live models or another migration.
type deviceAltIdKeySnapshot struct {
	table    string // in "event-management"
	name     string
	columns  string // as written in CREATE UNIQUE INDEX and as the catalog signature renders them
	where    string // the partial predicate
	replaces string
}

var deviceAltIdKey = deviceAltIdKeySnapshot{
	table:    "events",
	name:     "idx_events_tenant_device_alt_id",
	columns:  "tenant_id, device_token, alt_id, occurred_time",
	where:    "alt_id IS NOT NULL",
	replaces: "idx_events_tenant_alt_id",
}

// deviceAltIdKeyManualDrop and deviceAltIdKeyManualBuild are the two statements every refusal
// hands the operator. Unlike the per-device index's hand-run build it does NOT use
// timescaledb.transaction_per_chunk: that option is not accepted for this unique, partial
// index (the statement was refused against a real TimescaleDB), so the hand-run build is one
// statement that holds writers off for its whole run, as the migration's own build does for
// its bounded one. It has no IF NOT EXISTS on purpose: an index of that name already
// there, including an INVALID leftover, must fail the statement loudly rather than be skipped
// as though it were built; the DROP first removes exactly that.
const (
	deviceAltIdKeyManualDrop  = `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id`
	deviceAltIdKeyManualBuild = `CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ` +
		`ON "event-management".events (tenant_id, device_token, alt_id, occurred_time) ` +
		`WHERE alt_id IS NOT NULL`
)

const deviceAltIdKeyManualAdvice = "To finish it in place, run these on the event store's primary, outside " +
	"event-management's startup and at a quiet time: " + deviceAltIdKeyManualDrop + "; " +
	deviceAltIdKeyManualBuild + ". The DROP removes an unfinished index of that name, if one is there, " +
	"before the build. Then restart event-management, which removes the index it " +
	"replaces. The same statements can be run before upgrading"

// NewDeviceAltIdKeySchema scopes the alternate-id idempotency key to the DEVICE. The key was
// (tenant_id, alt_id, occurred_time), so two devices of one tenant that sent the same
// alternate id at the same instant collided: the first event was stored and the second was
// treated as its redelivery and silently dropped. An alternate id is the SENDER's message id,
// so it identifies a message only within one sender; the key is now
// (tenant_id, device_token, alt_id, occurred_time). A redelivery carries the same device,
// so it still collides.
//
// The new key is strictly weaker than the old (it adds a column), so no stored row can
// violate it. It is built under a NEW name before the old index goes, so there is no moment
// without an idempotency guard. Timescale requires a unique index on a hypertable to
// include the partitioning column, which both do (occurred_time).
//
// Properties, shared with the other index migrations of this area:
//
//  1. Catalog first. Done (new valid, old gone) takes no lock. A same-named index that is not
//     the one built here ends the migration before any DDL: it does not replace an index it
//     did not write. An INVALID leftover of an interrupted build is dropped and rebuilt.
//  2. Gated before any lock on chunks and not-yet-compressed rows, with the key rebuild's
//     bounds; past either it changes nothing and hands the operator the build to run by hand.
//  3. The build is ONE transaction under LOCK ... IN SHARE MODE (writers wait, readers do
//     not) with SET LOCAL lock_timeout and statement_timeout; a busy table is retried until
//     the budget is spent, then the next start resumes.
//  4. The old index is dropped in its own short bounded transaction, after the new one is
//     committed. Individually re-runnable at every step.
//
// There is no Rollback. A pod still running the previous release keeps working: it infers
// events_pkey as the events arbiter and, with the old index gone, only loses its stricter
// tenant-wide alternate-id guard.
func NewDeviceAltIdKeySchema() *gormigrate.Migration {
	return newDeviceAltIdKeySchema(timeLeadingKeysDefaultTiming)
}

func newDeviceAltIdKeySchema(timing timeLeadingKeysTiming) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261010000000",
		Migrate: func(tx *gorm.DB) error {
			return scopeAltIdKeyToDevice(tx, timing)
		},
	}
}

type deviceAltIdKeyState int

const (
	deviceAltIdKeyDone    deviceAltIdKeyState = iota // new valid, old absent
	deviceAltIdKeyDropOld                            // new valid, old present
	deviceAltIdKeyBuild                              // new absent
	deviceAltIdKeyRebuild                            // new present, right definition, INVALID: a leftover
)

func scopeAltIdKeyToDevice(db *gorm.DB, timing timeLeadingKeysTiming) error {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timing.budget)

	state, err := classifyDeviceAltIdKey(db)
	if err != nil {
		return err
	}
	if state == deviceAltIdKeyBuild || state == deviceAltIdKeyRebuild {
		if err := gateDeviceAltIdKey(db, timing); err != nil {
			return err
		}
		if state == deviceAltIdKeyRebuild {
			if err := dropIndexBounded(db, trimmedIndex{deviceAltIdKey.name, deviceAltIdKey.table},
				dropTiming(timing), deadline); err != nil {
				return err
			}
		}
		if err := buildDeviceAltIdKeyWithRetry(ctx, db, timing, deadline); err != nil {
			return err
		}
	}
	if err := dropIndexBounded(db, trimmedIndex{deviceAltIdKey.replaces, deviceAltIdKey.table},
		dropTiming(timing), deadline); err != nil {
		return fmt.Errorf("scoping the alternate-id key to the device: %s is built; removing %s failed: %w",
			deviceAltIdKey.name, deviceAltIdKey.replaces, err)
	}
	return nil
}

// gateDeviceAltIdKey refuses, changing nothing and locking nothing, a table with more chunks
// than maxChunks or more not-yet-compressed rows than maxRows.
func gateDeviceAltIdKey(db *gorm.DB, timing timeLeadingKeysTiming) error {
	tables := []string{deviceAltIdKey.table}
	counts, err := chunksPerTable(db, tables, timing.countTimeout)
	if err != nil {
		return fmt.Errorf("scoping the alternate-id key to the device: counting the chunks of events failed, "+
			"so nothing was changed; the next start tries again. Error: %w", err)
	}
	if over := overChunkCeiling(counts, tables, timing.maxChunks); over != "" {
		return fmt.Errorf("scoping the alternate-id key to the device: %s more than %d chunks, the most this "+
			"release builds an index over within one start, so this start changed nothing. %s. List the chunks with: %s",
			over, timing.maxChunks, deviceAltIdKeyManualAdvice, chunkListQuery)
	}
	rows, err := rowsToRekey(db, tables, timing.maxRows, timing.countTimeout)
	if err != nil {
		return fmt.Errorf("scoping the alternate-id key to the device: counting the rows to index failed, "+
			"so nothing was changed; the next start tries again. Error: %w", err)
	}
	if rows > timing.maxRows {
		return fmt.Errorf("scoping the alternate-id key to the device: more than %d rows of "+
			"\"event-management\".events not yet compressed would have to be indexed, longer than event-management "+
			"may spend starting, so this start changed nothing. %s", timing.maxRows, deviceAltIdKeyManualAdvice)
	}
	return nil
}

// classifyDeviceAltIdKey reads the catalog only, and takes no lock on the table. It therefore
// does not deparse the predicate with pg_get_expr(indpred, indrelid): that opens the indexed
// relation, so a re-run queued behind an ACCESS EXCLUSIVE lock on events (which it must not
// be) and hung. The predicate is recognised from its stored node tree instead: a single
// NULLTEST of the alt_id column, IS NOT NULL.
func classifyDeviceAltIdKey(db *gorm.DB) (deviceAltIdKeyState, error) {
	var found []struct {
		Signature string
		Method    string
		Unique    bool
		Partial   bool // a plain "alt_id IS NOT NULL" predicate, and no expression column
		Valid     bool
		OnEvents  bool
	}
	if err := db.Raw(`SELECT string_agg(a.attname, ', ' ORDER BY k.ord) AS signature,
		max(am.amname) AS method, bool_and(x.indisunique) AS "unique",
		bool_and(x.indpred IS NOT NULL AND x.indexprs IS NULL
			AND x.indpred::text ~ ('^\{NULLTEST :arg \{VAR :varno 1 :varattno '
				|| (SELECT ca.attnum FROM pg_attribute ca WHERE ca.attrelid = x.indrelid AND ca.attname = 'alt_id') || ' ')
			AND x.indpred::text ~ ':nulltesttype 1[ }]'
			AND x.indpred::text !~ 'BOOLEXPR') AS partial,
		bool_and(x.indisvalid) AS valid, bool_and(t.relname = ?) AS on_events
		FROM pg_class i
		JOIN pg_index x ON x.indexrelid = i.oid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_am am ON am.oid = i.relam
		JOIN pg_namespace n ON n.oid = i.relnamespace
		CROSS JOIN LATERAL unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
		WHERE n.nspname = 'event-management' AND i.relname = ?
		GROUP BY i.oid`, deviceAltIdKey.table, deviceAltIdKey.name).Scan(&found).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", deviceAltIdKey.name, err)
	}
	var oldPresent bool
	if err := db.Raw(`SELECT to_regclass(format('%I.%I', 'event-management', ?::text)) IS NOT NULL`,
		deviceAltIdKey.replaces).Scan(&oldPresent).Error; err != nil {
		return 0, fmt.Errorf("read the index %s of \"event-management\": %w", deviceAltIdKey.replaces, err)
	}

	valid := false
	if len(found) > 0 {
		f := found[0]
		if f.Signature != deviceAltIdKey.columns || f.Method != "btree" || !f.Unique || !f.OnEvents || !f.Partial {
			return 0, fmt.Errorf("an index named %s exists on \"event-management\" as (%s%s%s), which is "+
				"not the unique index this migration builds (%s WHERE %s). Nothing was changed: this migration does "+
				"not replace an index it did not write. Drop or rename it, then restart event-management.",
				deviceAltIdKey.name, f.Signature, map[bool]string{true: ", unique", false: ""}[f.Unique],
				map[bool]string{true: ", partial on alt_id", false: ", not partial on alt_id"}[f.Partial],
				deviceAltIdKey.columns, deviceAltIdKey.where)
		}
		valid = f.Valid
	}
	switch {
	case valid && oldPresent:
		return deviceAltIdKeyDropOld, nil
	case valid:
		return deviceAltIdKeyDone, nil
	case len(found) > 0:
		return deviceAltIdKeyRebuild, nil
	}
	return deviceAltIdKeyBuild, nil
}

// buildDeviceAltIdKeyWithRetry builds the index, retrying a busy table until the deadline.
func buildDeviceAltIdKeyWithRetry(ctx context.Context, db *gorm.DB, timing timeLeadingKeysTiming,
	deadline time.Time) error {
	for {
		if time.Now().Add(timing.lockAttempt + timing.minBuild).After(deadline) {
			return fmt.Errorf("scoping the alternate-id key to the device stopped because this start's budget of "+
				"%s was spent. Nothing is half-applied, and this migration continues from here on the next start.",
				timing.budget)
		}
		err := buildDeviceAltIdKey(db, timing, deadline)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("build %s on \"event-management\".events: %w", deviceAltIdKey.name, ctx.Err())
		}
		if isLockTableFull(err) {
			return fmt.Errorf("building %s on \"event-management\".events: the server ran out of lock slots (the "+
				"build locks the table and every chunk of it). Raise max_locks_per_transaction on the event store and "+
				"restart; nothing was changed. Error: %w", deviceAltIdKey.name, err)
		}
		var pgErr *pgconn.PgError
		busy := errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01" || pgErr.Code == "57014")
		if !busy || time.Now().Add(timing.pause+timing.lockAttempt+timing.minBuild).After(deadline) {
			return fmt.Errorf("building %s on \"event-management\".events failed and was rolled back; the next "+
				"start tries again. If it fails the same way on every start: %s. Error: %w",
				deviceAltIdKey.name, deviceAltIdKeyManualAdvice, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("build %s on \"event-management\".events: %w", deviceAltIdKey.name, ctx.Err())
		case <-time.After(timing.pause):
		}
	}
}

// buildDeviceAltIdKey builds the index inside ONE transaction: lock events and every
// compressed relation IN SHARE MODE under a bounded wait, then create the index.
func buildDeviceAltIdKey(db *gorm.DB, timing timeLeadingKeysTiming, deadline time.Time) error {
	return db.Transaction(func(t *gorm.DB) error {
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
		var compressed []string
		if err := t.Raw(`SELECT format('%I.%I', cc.schema_name, cc.table_name)
			FROM _timescaledb_catalog.chunk c
			JOIN _timescaledb_catalog.chunk cc ON cc.id = c.compressed_chunk_id
			JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
			WHERE h.schema_name = 'event-management' AND h.table_name = ?`,
			deviceAltIdKey.table).Scan(&compressed).Error; err != nil {
			return err
		}
		targets := append([]string{`"event-management".` + deviceAltIdKey.table}, compressed...)
		if err := t.Exec(`LOCK TABLE ` + strings.Join(targets, ", ") + ` IN SHARE MODE`).Error; err != nil {
			return err
		}
		allowance, _, err := buildAllowance(timing, deadline, time.Now())
		if err != nil {
			return err
		}
		build, err := timeoutSetting(allowance)
		if err != nil {
			return err
		}
		if err := t.Exec(`SET LOCAL statement_timeout = '` + build + `'`).Error; err != nil {
			return err
		}
		return t.Exec(fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS %s ON "event-management".%s (%s) WHERE %s`,
			deviceAltIdKey.name, deviceAltIdKey.table, deviceAltIdKey.columns, deviceAltIdKey.where)).Error
	})
}
