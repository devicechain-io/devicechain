// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the chunk ceiling both startup migrations that lock every chunk of a
// table (the index trim and the time-leading key rebuild) check before they lock anything,
// and for the remedy the refusal hands the operator. Run as postgres_integration_test.go
// describes.
package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// backdateEvents adds n rows to table, one per chunk: row g (from from to from+n-1) dated g
// steps before 2026-07-01. Under the baseline's 7-day chunk interval a step of 8 days puts
// every row in a chunk of its own, so the table gains n chunks. This is the shape a device
// sending readings dated far in the past leaves behind.
func backdateEvents(t *testing.T, db *gorm.DB, table string, from, n int, step time.Duration) {
	t.Helper()
	var insert string
	switch table {
	case "events":
		insert = `INSERT INTO "event-management".events (tenant_id, event_id, device_token, event_type, occurred_time, source)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), 'dev-old', ?::bigint, ts, 'mqtt' FROM src`
	case "measurement_events":
		insert = `INSERT INTO "event-management".measurement_events
			(tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), sha256(convert_to('oldp:' || g::text, 'UTF8')),
			'dev-old', ?::bigint, ts, 'temperature', 20, 'C', 'DOUBLE' FROM src`
	case "location_events":
		insert = `INSERT INTO "event-management".location_events
			(tenant_id, event_id, payload_id, device_token, event_type, occurred_time, latitude, longitude)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), sha256(convert_to('oldp:' || g::text, 'UTF8')),
			'dev-old', ?::bigint, ts, 10, 20 FROM src`
	case "alert_events":
		insert = `INSERT INTO "event-management".alert_events
			(tenant_id, event_id, payload_id, device_token, event_type, occurred_time, type, level, message, source)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), sha256(convert_to('oldp:' || g::text, 'UTF8')),
			'dev-old', ?::bigint, ts, 'overheat', 1, 'hot', 'device' FROM src`
	case "event_anchors":
		insert = `INSERT INTO "event-management".event_anchors
			(tenant_id, event_id, device_token, event_type, occurred_time, anchor_type, anchor_token)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), 'dev-old', ?::bigint, ts, 'area', 'area-0' FROM src`
	case "state_change_events":
		insert = `INSERT INTO "event-management".state_change_events
			(tenant_id, event_id, device_token, event_type, occurred_time, state, reason, session_id)
			SELECT 'acme', sha256(convert_to('old:' || g::text, 'UTF8')), 'dev-old', ?::bigint, ts, 'ONLINE', 'connect', g FROM src`
	default:
		t.Fatalf("no backdating seed for %s", table)
	}
	src := `WITH src AS (SELECT g, TIMESTAMPTZ '2026-07-01' - g * make_interval(secs => ?::double precision) AS ts
		FROM generate_series(?::int, ?::int) g) `
	require.NoErrorf(t, db.Exec(src+insert, step.Seconds(), from, from+n-1, int(esmodel.Measurement)).Error, "backdate %s", table)
}

// chunkCount reads how many chunks table has, through the public view (not the catalog
// table the gate reads, so the two are checked against each other).
func chunkCount(t *testing.T, db *gorm.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.Raw(`SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = ?`, table).Scan(&n).Error)
	return n
}

// holdShared opens a session holding table ACCESS SHARE, as a long reader does: it blocks
// the ACCESS EXCLUSIVE either migration takes to lock the table, and nothing else, so the
// row count both used to run first still runs. It returns the release.
func holdShared(t *testing.T, inst, table string) func() {
	t.Helper()
	holder := connectInstance(t, inst)
	_, err := holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".`+table+` IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	released := false
	release := func() {
		if !released {
			released = true
			_, err := holder.Exec(context.Background(), `ROLLBACK`)
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() {
		if !released {
			_, _ = holder.Exec(context.Background(), `ROLLBACK`)
		}
	})
	return release
}

// compressAllBut enables compression on table and compresses every chunk but the newest keep.
func compressAllBut(t *testing.T, db *gorm.DB, table string, keep int) {
	t.Helper()
	require.NoError(t, db.Exec(enableCompressionStmt(table)).Error)
	require.NoError(t, db.Exec(`SELECT compress_chunk(format('%I.%I', chunk_schema, chunk_name)::regclass)
		FROM (SELECT chunk_schema, chunk_name FROM timescaledb_information.chunks
		      WHERE hypertable_schema = 'event-management' AND hypertable_name = ?
		      ORDER BY range_end DESC OFFSET ?) c`, table, keep).Error)
}

func pkeyNote(t *testing.T, db *gorm.DB) *string {
	t.Helper()
	var note *string
	require.NoError(t, db.Raw(`SELECT obj_description('"event-management".events_pkey'::regclass, 'pg_class')`).Scan(&note).Error)
	return note
}

// TestIntegrationTimeLeadingKeysRefuseTooManyChunksBeforeLocking: a table with more than
// eventStoreMaxChunks chunks is refused by the rebuild, at once, before it takes any lock, with a message
// naming the table, its chunk count, the query that lists the chunks and the drop_chunks
// remedy; nothing is changed and nothing is marked, so the next start decides again.
//
// It runs the shipped default timing, and a long reader holds events throughout. Before the
// gate, the rebuild counted rows (one tiny scan per chunk), then queued its lock
// behind the reader and spent its whole minute of budget there before reporting a busy table,
// the wrong diagnosis; on a large enough table it ran out of time instead and marked the key
// too slow, which refuses every later start.
//
// It also re-takes, on every run, the measurement the ceiling rests on: events at exactly
// eventStoreMaxChunks chunks, all but the newest 50 compressed (the shape of a long-lived
// instance), goes through the trim and then the rebuild at the shipped default timing, and
// both apply, the rebuild inside one table's allowance. One seed serves both halves, because
// hundreds of chunks are what makes this suite slow.
func TestIntegrationTimeLeadingKeysRefuseTooManyChunksBeforeLocking(t *testing.T) {
	inst := freshInstance(t, "itrekeychunks")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, NewIndexTrimSchema().ID))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	backdateEvents(t, sys, "events", 1, eventStoreMaxChunks-2, 8*24*time.Hour)
	require.Equal(t, eventStoreMaxChunks, chunkCount(t, sys, "events"))
	compressAllBut(t, sys, "events", 50)

	// The trim, at the ceiling: it applies.
	start := time.Now()
	require.NoError(t, NewIndexTrimSchema().Migrate(sys), "the trim applies at the ceiling")
	t.Logf("index trim over %d chunks of events: %s", eventStoreMaxChunks, time.Since(start))
	assert.Less(t, time.Since(start), indexTrimDefaultTiming.budget)

	// Two chunks more, older than every other, and the rebuild refuses.
	backdateEvents(t, sys, "events", eventStoreMaxChunks-1, 2, 8*24*time.Hour)
	require.Equal(t, eventStoreMaxChunks+2, chunkCount(t, sys, "events"))

	release := holdShared(t, inst, "events")
	start = time.Now()
	err := NewTimeLeadingKeysSchema().Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "the refusal is decided from the catalog, before any lock: %v", err)
	for _, want := range []string{"rebuilding the event store's identity keys",
		fmt.Sprintf("events (%d) has more than %d chunks", eventStoreMaxChunks+2, eventStoreMaxChunks),
		"drop_chunks", "timescaledb_information.chunks", "every tenant",
		"no table has been re-keyed yet"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "measurement_events (", "only a table over the ceiling is named")
	assertAfterTrimIndexes(t, sys, "after the chunk refusal")
	assert.Nil(t, pkeyNote(t, sys), "a chunk refusal leaves no marker: the next start decides again")

	// Negative control: the same table and the same holder, under a ceiling it is within.
	// The run now counts the rows and then really waits on the holder's lock, ending busy
	// after at least one lock attempt. That is what makes "under a second" above mean that
	// no lock was attempted, rather than that the holder blocks nothing.
	timing := timeLeadingKeysTiming{lockTimeout: 200 * time.Millisecond, lockAttempt: 500 * time.Millisecond,
		pause: 100 * time.Millisecond, tableBuild: 30 * time.Second, minBuild: 10 * time.Millisecond,
		budget: 10 * time.Second, countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: 2000,
		buildMemory: "64MB"}
	start = time.Now()
	err = newTimeLeadingKeysSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), timing.lockAttempt)
	assert.Contains(t, err.Error(), "stayed busy")
	assert.NotContains(t, err.Error(), "chunks, the most")
	release()

	// Back at the ceiling (the two oldest chunks removed), the rebuild applies at the default
	// timing, inside one table's allowance.
	require.NoError(t, sys.Exec(`SELECT drop_chunks('"event-management".events', older_than => (
		SELECT range_end FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = 'events'
		ORDER BY range_end LIMIT 1 OFFSET 1))`).Error)
	require.Equal(t, eventStoreMaxChunks, chunkCount(t, sys, "events"))
	start = time.Now()
	require.NoError(t, NewTimeLeadingKeysSchema().Migrate(sys), "the rebuild applies at the ceiling")
	rekey := time.Since(start)
	t.Logf("identity-key rebuild over %d chunks of events: %s", eventStoreMaxChunks, rekey)
	assert.Less(t, rekey, timeLeadingKeysDefaultTiming.tableBuild,
		"the rebuild at the ceiling fits one table's allowance")
	assertFinalIndexes(t, sys, 2)
	assert.Nil(t, pkeyNote(t, sys))
}

// TestIntegrationIndexTrimRefusesTooManyChunksBeforeLocking is the same gate on the trim,
// which runs first in the same start and locks every chunk of a table for each index it
// drops; then the remedy the refusal names, run verbatim from the upgrade notes, after which
// the whole chain applies.
//
// Before the gate the trim dropped the indexes of every table it reached first, then waited
// on the reader for its whole budget and reported a busy table; past roughly nine thousand
// chunks every attempt would have run out of its five-second bound and reported a busy table
// on every start, whatever held it.
func TestIntegrationIndexTrimRefusesTooManyChunksBeforeLocking(t *testing.T) {
	inst := freshInstance(t, "ittrimchunks")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, NewIndexTrimSchema().ID))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	backdateEvents(t, sys, "events", 1, eventStoreMaxChunks, 8*24*time.Hour)
	// The other five tables get more than one batch of the remedy's to drop, so its loop is
	// exercised on every table, not only the one over the ceiling.
	for _, table := range eventStoreTables[1:] {
		backdateEvents(t, sys, table, 1, 120, 8*24*time.Hour)
	}
	require.Equal(t, eventStoreMaxChunks+2, chunkCount(t, sys, "events"))

	release := holdShared(t, inst, "events")
	start := time.Now()
	err := NewIndexTrimSchema().Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "the refusal is decided from the catalog, before any lock: %v", err)
	for _, want := range []string{"dropping the event store's unused indexes",
		fmt.Sprintf("events (%d) has more than %d chunks", eventStoreMaxChunks+2, eventStoreMaxChunks),
		"drop_chunks", "timescaledb_information.chunks", "no index was dropped"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "measurement_events (", "only a table over the ceiling is named")
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, preTrimNames(table), sortedNames(hypertableIndexes(t, sys, table)),
			"no index is dropped from %s by a refused start", table)
	}
	release()

	// The remedy, verbatim, as an operator runs it in psql: simple protocol, autocommit, so
	// its COMMIT between batches is legal.
	const cutoff = "2024-01-01"
	kept := map[string]int64{}
	for _, table := range eventStoreTables {
		var n int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".`+table+
			` WHERE occurred_time >= TIMESTAMPTZ '`+cutoff+`'`).Scan(&n).Error)
		require.Positive(t, n)
		kept[table] = n
	}
	remedy := documentedDropChunks(t)
	require.Contains(t, remedy, "TIMESTAMPTZ '"+cutoff+"'", "the documented example cutoff is the one this test checks")
	conn := connectInstance(t, inst)
	_, err = conn.PgConn().Exec(context.Background(), remedy).ReadAll()
	require.NoError(t, err, "the documented remedy runs as written")
	for _, table := range eventStoreTables {
		var old, n int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.chunks
			WHERE hypertable_schema = 'event-management' AND hypertable_name = ?
			AND range_end <= TIMESTAMPTZ '`+cutoff+`'`, table).Scan(&old).Error)
		assert.Zerof(t, old, "%s keeps no chunk wholly before the cutoff", table)
		require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".`+table+
			` WHERE occurred_time >= TIMESTAMPTZ '`+cutoff+`'`).Scan(&n).Error)
		assert.Equalf(t, kept[table], n, "%s keeps every row from the cutoff on", table)
	}
	assert.Less(t, chunkCount(t, sys, "events"), eventStoreMaxChunks)

	// The next start applies the whole chain, the trim and the rebuild, at the default timing.
	after := newPostgresManagerWith(t, inst, Migrations)
	assertFinalIndexes(t, systemDB(after), 2)
}

// documentedDropChunks returns the batched drop_chunks procedure the upgrade notes publish,
// so this test runs exactly what an operator is told to run. The Spanish page must carry the
// identical statement: two copies of a procedure are one more chance to publish a broken one.
func documentedDropChunks(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "docs")
	blocks := func(path string) []string {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		var out []string
		for _, m := range regexp.MustCompile("(?s)```sql\n(.*?)```").FindAllStringSubmatch(string(body), -1) {
			if strings.Contains(m[1], "drop_chunks(") {
				out = append(out, m[1])
			}
		}
		return out
	}
	en := blocks(filepath.Join(root, "docs", "deployment", "releases-and-upgrades.md"))
	require.Len(t, en, 1, "the English upgrade notes publish exactly one drop_chunks procedure")
	es := blocks(filepath.Join(root, "i18n", "es", "docusaurus-plugin-content-docs", "current", "deployment",
		"releases-and-upgrades.md"))
	require.Len(t, es, 1, "the Spanish upgrade notes publish exactly one drop_chunks procedure")
	require.Equal(t, en[0], es[0], "both locales publish the same statement")
	return en[0]
}

// TestIntegrationChunkCeilingIsPerTableAndInclusive pins the ceiling's three properties on a
// small table: events has 6 chunks, half of them compressed, every other table 2. A ceiling
// of 5 refuses, naming events alone; a ceiling of 6 applies. Counting only uncompressed
// chunks (3) would apply at 5; summing across tables (16) would refuse at 6; a ceiling that
// refused AT the limit would refuse at 6.
func TestIntegrationChunkCeilingIsPerTableAndInclusive(t *testing.T) {
	mgr := newPostgresManagerWith(t, freshInstance(t, "itceilingedge"), migrationsBefore(t, rekeyID()))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	backdateEvents(t, sys, "events", 1, 4, 8*24*time.Hour)
	require.Equal(t, 6, chunkCount(t, sys, "events"))
	compressAllBut(t, sys, "events", 3)

	timing := testRekeyTiming
	timing.maxChunks = 5
	err := newTimeLeadingKeysSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events (6) has more than 5 chunks")
	for _, table := range eventStoreTables[1:] {
		assert.NotContains(t, err.Error(), table+" (")
	}
	assertAfterTrimIndexes(t, sys, "after the refusal at 5")

	timing.maxChunks = 6
	require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys))
	assertFinalIndexes(t, sys, 2)
}

// TestIntegrationChunkCeilingGatesADropOnlyTable: a table whose key is already rebuilt but
// whose redundant tenant-time index is back still has that index dropped, and dropping an
// index from a hypertable locks the table and every chunk of it just as a swap does. So the
// ceiling covers every table not yet done, not only the ones that build: measurement_events,
// drop-only with 6 chunks, is refused at a ceiling of 5, at once and with the index still in
// place, and applies at 6.
func TestIntegrationChunkCeilingGatesADropOnlyTable(t *testing.T) {
	inst := freshInstance(t, "itceilingdroponly")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	backdateEvents(t, sys, "measurement_events", 1, 4, 8*24*time.Hour)
	require.Equal(t, 6, chunkCount(t, sys, "measurement_events"))

	// The drop-only state: the key is new, the redundant index is back.
	require.NoError(t, sys.Exec(`CREATE INDEX measurement_events_tenant_id_occurred_time_idx
		ON "event-management".measurement_events (tenant_id, occurred_time DESC)`).Error)
	redundantPresent := func() bool {
		var present bool
		require.NoError(t, sys.Raw(`SELECT to_regclass('"event-management".measurement_events_tenant_id_occurred_time_idx')
			IS NOT NULL`).Scan(&present).Error)
		return present
	}
	require.True(t, redundantPresent())

	timing := testRekeyTiming
	timing.maxChunks = 5
	start := time.Now()
	err := newTimeLeadingKeysSchema(timing).Migrate(sys)
	require.Error(t, err, "a drop-only table over the ceiling is refused")
	assert.Less(t, time.Since(start), time.Second, "the refusal is decided from the catalog: %v", err)
	for _, want := range []string{"rebuilding the event store's identity keys",
		"measurement_events (6) has more than 5 chunks", "drop_chunks"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.True(t, redundantPresent(), "a refused start drops nothing")

	timing.maxChunks = 6
	require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys))
	assert.False(t, redundantPresent(), "within the ceiling the drop-only table has its index dropped")
	assertFinalIndexes(t, sys, 2)
}
