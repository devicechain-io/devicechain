// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the event-store index trim (NewIndexTrimSchema), against the
// pinned TimescaleDB image. Run as postgres_integration_test.go describes.
package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// keptIndexes is every index the six event hypertables carry after the trim, by
// table, as pg_indexes renders its definition. Transcribed from the golden schema, so
// a changed column list or order fails as surely as an extra or missing name.
var keptIndexes = map[string]map[string]string{
	"events": {
		"events_pkey":                           `CREATE UNIQUE INDEX events_pkey ON "event-management".events USING btree (tenant_id, event_id, occurred_time)`,
		"events_device_token_occurred_time_idx": `CREATE INDEX events_device_token_occurred_time_idx ON "event-management".events USING btree (device_token, occurred_time DESC)`,
		"events_tenant_id_occurred_time_idx":    `CREATE INDEX events_tenant_id_occurred_time_idx ON "event-management".events USING btree (tenant_id, occurred_time DESC)`,
		"idx_events_tenant_alt_id":              `CREATE UNIQUE INDEX idx_events_tenant_alt_id ON "event-management".events USING btree (tenant_id, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`,
	},
	"location_events": {
		"location_events_tenant_id_occurred_time_idx": `CREATE INDEX location_events_tenant_id_occurred_time_idx ON "event-management".location_events USING btree (tenant_id, occurred_time DESC)`,
		"uq_location_events_idem":                     `CREATE UNIQUE INDEX uq_location_events_idem ON "event-management".location_events USING btree (tenant_id, payload_id, occurred_time)`,
	},
	"measurement_events": {
		"measurement_events_occurred_time_idx":           `CREATE INDEX measurement_events_occurred_time_idx ON "event-management".measurement_events USING btree (occurred_time DESC)`,
		"measurement_events_tenant_id_occurred_time_idx": `CREATE INDEX measurement_events_tenant_id_occurred_time_idx ON "event-management".measurement_events USING btree (tenant_id, occurred_time DESC)`,
		"idx_measurement_tenant_device_name_time":        `CREATE INDEX idx_measurement_tenant_device_name_time ON "event-management".measurement_events USING btree (tenant_id, device_token, name, occurred_time DESC)`,
		"uq_measurement_events_idem":                     `CREATE UNIQUE INDEX uq_measurement_events_idem ON "event-management".measurement_events USING btree (tenant_id, payload_id, occurred_time)`,
	},
	"alert_events": {
		"alert_events_tenant_id_occurred_time_idx": `CREATE INDEX alert_events_tenant_id_occurred_time_idx ON "event-management".alert_events USING btree (tenant_id, occurred_time DESC)`,
		"uq_alert_events_idem":                     `CREATE UNIQUE INDEX uq_alert_events_idem ON "event-management".alert_events USING btree (tenant_id, payload_id, occurred_time)`,
	},
	"event_anchors": {
		"idx_event_anchors_lookup": `CREATE INDEX idx_event_anchors_lookup ON "event-management".event_anchors USING btree (tenant_id, anchor_type, anchor_token, occurred_time DESC)`,
		"uq_event_anchors_idem":    `CREATE UNIQUE INDEX uq_event_anchors_idem ON "event-management".event_anchors USING btree (tenant_id, event_id, occurred_time, anchor_type, anchor_token)`,
	},
	"state_change_events": {
		"uq_state_change_events_idem": `CREATE UNIQUE INDEX uq_state_change_events_idem ON "event-management".state_change_events USING btree (tenant_id, device_token, occurred_time, state, session_id)`,
	},
}

// hypertableIndexes reads every index on one event hypertable: name → definition.
func hypertableIndexes(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var rows []struct{ Indexname, Indexdef string }
	require.NoError(t, db.Raw(`SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = 'event-management' AND tablename = ?`, table).Scan(&rows).Error)
	out := map[string]string{}
	for _, r := range rows {
		out[r.Indexname] = r.Indexdef
	}
	return out
}

// chunkIndexSuffixes reads, for every chunk of one hypertable, the set of hypertable
// index names its own indexes are copies of.
func chunkIndexSuffixes(t *testing.T, db *gorm.DB, table string) map[string][]string {
	t.Helper()
	var rows []struct{ ChunkName, Indexname string }
	require.NoError(t, db.Raw(`SELECT c.chunk_name, i.indexname
		FROM timescaledb_information.chunks c
		JOIN pg_indexes i ON i.schemaname = c.chunk_schema AND i.tablename = c.chunk_name
		WHERE c.hypertable_schema = 'event-management' AND c.hypertable_name = ?`, table).Scan(&rows).Error)
	// Every name this table's indexes have had: a chunk copy is resolved to one of them
	// (see chunkIndexIs); a copy of anything else keeps its raw name and so fails a
	// comparison rather than hiding.
	known := preTrimNames(table)
	for name := range hypertableIndexes(t, db, table) {
		known = append(known, name)
	}
	out := map[string][]string{}
	for _, r := range rows {
		name := r.Indexname
		for _, k := range known {
			if chunkIndexIs(r.Indexname, k) {
				name = k
				break
			}
		}
		out[r.ChunkName] = append(out[r.ChunkName], name)
	}
	for c := range out {
		sort.Strings(out[c])
	}
	return out
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// preTrimNames is the index-name set each table carried before the trim.
func preTrimNames(table string) []string {
	names := sortedNames(keptIndexes[table])
	for _, idx := range indexTrimDropped {
		if idx.table == table {
			names = append(names, idx.name)
		}
	}
	sort.Strings(names)
	return names
}

// assertChunkSets asserts every chunk of every event hypertable carries exactly want(table),
// and that each table HAS chunks — an empty map would pass any comparison.
func assertChunkSets(t *testing.T, db *gorm.DB, minChunks int, want func(string) []string) {
	t.Helper()
	for _, table := range LifecycleHypertables {
		chunks := chunkIndexSuffixes(t, db, table)
		require.GreaterOrEqualf(t, len(chunks), minChunks, "%s: too few chunks for this check to mean anything", table)
		for chunk, got := range chunks {
			assert.Equalf(t, want(table), got, "%s chunk %s", table, chunk)
		}
	}
}

// seedEventStore writes, for every tenant × device × stamp, one row into each of the six
// hypertables, via SQL so a large seed stays fast. Stamp g of a device is
// start + g*step. Every 50th stamp of a device is an ALERT event rather than a
// measurement, so a device + event-type read is selective.
func seedEventStore(t *testing.T, db *gorm.DB, tenants []string, devices, stamps int, start time.Time, step time.Duration) {
	t.Helper()
	src := `WITH src AS (
  SELECT tn AS tenant, 'dev-' || d::text AS device,
         ?::timestamptz + g * make_interval(secs => ?::double precision) AS ts,
         CASE WHEN g % 50 = 49 THEN ?::bigint ELSE ?::bigint END AS etype,
         sha256(convert_to(tn || ':' || d::text || ':' || g::text, 'UTF8')) AS eid,
         d, g
  FROM unnest(string_to_array(?::text, ',')) AS tn,
       generate_series(0, ?::int - 1) d, generate_series(0, ?::int - 1) g)
`
	args := []any{start, step.Seconds(), int(esmodel.Alert), int(esmodel.Measurement),
		strings.Join(tenants, ","), devices, stamps}
	for table, insert := range map[string]string{
		"events": `INSERT INTO "event-management".events
  (tenant_id, event_id, device_token, event_type, occurred_time, source)
  SELECT tenant, eid, device, etype, ts, 'mqtt' FROM src`,
		"measurement_events": `INSERT INTO "event-management".measurement_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
  SELECT tenant, eid, sha256(eid), device, etype, ts, 'temperature', 20 + g % 10, 'C', 'DOUBLE' FROM src`,
		"location_events": `INSERT INTO "event-management".location_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, latitude, longitude)
  SELECT tenant, eid, sha256(eid), device, 1, ts, 10, 20 FROM src`,
		"alert_events": `INSERT INTO "event-management".alert_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, type, level, message, source)
  SELECT tenant, eid, sha256(eid), device, 3, ts, 'overheat', 1, 'hot', 'device' FROM src`,
		"event_anchors": `INSERT INTO "event-management".event_anchors
  (tenant_id, event_id, device_token, event_type, occurred_time, anchor_type, anchor_token)
  SELECT tenant, eid, device, etype, ts, 'area', 'area-' || (d % 4)::text FROM src`,
		"state_change_events": `INSERT INTO "event-management".state_change_events
  (tenant_id, event_id, device_token, event_type, occurred_time, state, reason, session_id)
  SELECT tenant, eid, device, 4, ts, 'ONLINE', 'connect', g FROM src`,
	} {
		require.NoErrorf(t, db.Exec(src+insert, args...).Error, "seed %s", table)
	}
}

// twoChunkSeed is the small seed: one tenant, two devices, a row on 2026-08-01 and one on
// 2026-08-20 — two chunks of every table under the baseline's 7-day interval.
func twoChunkSeed(t *testing.T, db *gorm.DB) {
	seedEventStore(t, db, []string{"acme"}, 2, 2,
		time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), 19*24*time.Hour)
}

func systemDB(mgr *rdb.RdbManager) *gorm.DB {
	return mgr.Database.WithContext(core.WithSystemContext(context.Background()))
}

// TestIntegrationEventStoreKeepsOnlyTheIndexesItsQueriesUse is the by-value test of the
// trim: after the real chain, each event hypertable — and every one of its chunks —
// carries exactly the kept set. Before the trim this fails listing the twelve dropped
// names on the hypertables and on every chunk.
func TestIntegrationEventStoreKeepsOnlyTheIndexesItsQueriesUse(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "ittrimkept"))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	for _, table := range LifecycleHypertables {
		assert.Equalf(t, keptIndexes[table], hypertableIndexes(t, sys, table), "indexes on %s", table)
	}
	assertChunkSets(t, sys, 2, func(table string) []string { return sortedNames(keptIndexes[table]) })

	// Negative control: the reader reads. An index the kept set does not name must make
	// the comparison differ.
	require.NoError(t, sys.Exec(`CREATE INDEX t3_probe ON "event-management".alert_events (source)`).Error)
	assert.NotEqual(t, keptIndexes["alert_events"], hypertableIndexes(t, sys, "alert_events"),
		"the index reader must see an index outside the kept set")
	require.NoError(t, sys.Exec(`DROP INDEX "event-management".t3_probe`).Error)
}

// TestIntegrationIndexTrimAppliesOverCompressedChunks upgrades a database whose older
// chunks are already compressed — the shape every long-lived instance has — and proves
// the drop reaches the compressed chunks, and that the reads still return every row.
func TestIntegrationIndexTrimAppliesOverCompressedChunks(t *testing.T) {
	inst := freshInstance(t, "ittrimz")
	before := newPostgresManagerWith(t, inst, Migrations[:len(Migrations)-1])
	sys := systemDB(before)
	twoChunkSeed(t, sys)

	oldChunk := map[string]string{}
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(enableCompressionStmt(table)).Error, "enable compression on %s", table)
		var chunk string
		require.NoError(t, sys.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
			FROM timescaledb_information.chunks
			WHERE hypertable_schema = 'event-management' AND hypertable_name = ?
			ORDER BY range_start LIMIT 1`, table).Scan(&chunk).Error)
		require.NotEmpty(t, chunk)
		oldChunk[table] = chunk
		require.NoError(t, sys.Exec(`SELECT compress_chunk(?::regclass)`, chunk).Error, "compress %s", chunk)

		// Preconditions, read from the rows rather than from is_compressed (see
		// NewLocationFixFieldsSchema): the chunk is flagged compressed and holds no raw rows.
		var status int
		require.NoError(t, sys.Raw(`SELECT c.status FROM _timescaledb_catalog.chunk c
			WHERE format('%I.%I', c.schema_name, c.table_name) = ?`, chunk).Scan(&status).Error)
		require.Equalf(t, 1, status&1, "%s must be compressed", chunk)
		var raw int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM ONLY `+chunk).Scan(&raw).Error)
		require.Zerof(t, raw, "%s must hold no uncompressed rows", chunk)
	}
	// The test starts from the OLD shape, on every chunk, compressed one included.
	assertChunkSets(t, sys, 2, preTrimNames)

	after := newPostgresManagerWith(t, inst, Migrations)
	sys = systemDB(after)
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, keptIndexes[table], hypertableIndexes(t, sys, table), "indexes on %s", table)
	}
	assertChunkSets(t, sys, 2, func(table string) []string { return sortedNames(keptIndexes[table]) })

	api := NewApi(after)
	ctx := core.WithTenant(context.Background(), "acme")
	d0 := "dev-0"
	newer := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	assertReads := func(stage string) {
		byDevice, err := api.Events(ctx, EventSearchCriteria{DeviceToken: &d0})
		require.NoError(t, err)
		require.Lenf(t, byDevice.Results, 2, "%s: device read", stage)
		assert.True(t, byDevice.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, byDevice.Results[1].OccurredTime.Equal(older), stage)
		assert.EqualValues(t, 2, byDevice.Pagination.TotalRecords, stage)

		all, err := api.Events(ctx, EventSearchCriteria{})
		require.NoError(t, err)
		require.Lenf(t, all.Results, 4, "%s: tenant read", stage)
		assert.True(t, all.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, all.Results[3].OccurredTime.Equal(older), stage)

		ms, err := api.MeasurementEvents(ctx, EventSearchCriteria{})
		require.NoError(t, err)
		require.Lenf(t, ms.Results, 4, "%s: measurement read", stage)
		assert.True(t, ms.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, ms.Results[3].OccurredTime.Equal(older), stage)
	}
	assertReads("compressed")

	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, oldChunk[table]).Error)
	}
	assertChunkSets(t, sys, 2, func(table string) []string { return sortedNames(keptIndexes[table]) })
	assertReads("decompressed")
}

// connectInstance opens a raw pgx connection to one instance database.
func connectInstance(t *testing.T, inst string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		envOr("DC_IT_PGUSER", "postgres"), envOr("DC_IT_PGPASSWORD", "devicechain"),
		envOr("DC_IT_PGHOST", "localhost"), envOr("DC_IT_PGPORT", "5432"), inst))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// waitForLockWaiter polls until some session waits on a heavyweight lock, so a test can
// act while a DROP is mid-attempt rather than before it started.
func waitForLockWaiter(t *testing.T, conn *pgx.Conn, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var n int
		require.NoError(t, conn.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n))
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no session started waiting on a lock within %s", within)
}

// TestIntegrationIndexTrimWaitsBoundedlyForALockAndResumes holds a lock on one table
// across the migration and proves: the migration fails loudly within its budget, having
// dropped exactly the indexes before the busy table; writes to the busy table wait only
// one bounded attempt; the next run resumes and finishes; and no timeout outlives its
// transaction on the connection that ran it.
func TestIntegrationIndexTrimWaitsBoundedlyForALockAndResumes(t *testing.T) {
	timing := indexTrimTiming{lockTimeout: 500 * time.Millisecond, statementTimeout: 800 * time.Millisecond,
		pause: 200 * time.Millisecond, budget: 3 * time.Second}

	// Negative control: with nothing holding a lock, the same migration on the same
	// shape of database finishes at once — so it is the lock, not the harness, that makes
	// the run below wait.
	{
		mgr := newPostgresManagerWith(t, freshInstance(t, "ittrimfree"), Migrations[:len(Migrations)-1])
		sys := systemDB(mgr)
		twoChunkSeed(t, sys)
		start := time.Now()
		require.NoError(t, newIndexTrimSchema(timing).Migrate(sys))
		assert.Less(t, time.Since(start), 1500*time.Millisecond, "an uncontended trim must not wait")
	}

	inst := freshInstance(t, "ittrimlock")
	mgr := newPostgresManagerWith(t, inst, Migrations[:len(Migrations)-1])
	sqlDB, err := mgr.Database.DB()
	require.NoError(t, err)
	// ONE connection, so the SHOW at the end reads the very session the migration ran on.
	sqlDB.SetMaxOpenConns(1)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	holder := connectInstance(t, inst)
	writer := connectInstance(t, inst)
	observer := connectInstance(t, inst)
	_, err = holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".state_change_events IN ACCESS SHARE MODE`)
	require.NoError(t, err)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- newIndexTrimSchema(timing).Migrate(sys) }()

	// While the DROP waits on the held table, a write to that table waits at most one
	// bounded attempt — not for the holder's whole transaction.
	waitForLockWaiter(t, observer, 2*time.Second)
	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	wstart := time.Now()
	_, err = writer.Exec(wctx, `INSERT INTO "event-management".state_change_events
		(tenant_id, event_id, device_token, event_type, occurred_time, state, session_id)
		VALUES ('acme', '\x01', 'dev-9', 4, '2026-08-02T00:00:00Z', 'ONLINE', 99)`)
	cancel()
	require.NoError(t, err, "a write to the busy table must complete while the migration retries")
	assert.Less(t, time.Since(wstart), timing.statementTimeout+400*time.Millisecond,
		"a write must wait at most one bounded attempt")

	var migErr error
	select {
	case migErr = <-done:
	case <-time.After(5 * time.Second):
		_, _ = holder.Exec(context.Background(), `ROLLBACK`)
		t.Fatal("the migration did not return within 5 s: the retry is not bounded")
	}
	elapsed := time.Since(start)
	require.Error(t, migErr, "a table busy past the budget must fail the migration")
	assert.Less(t, elapsed, timing.budget+time.Second)
	msg := migErr.Error()
	assert.Contains(t, msg, "idx_event-management_state_change_events_tenant_id")
	assert.Contains(t, msg, `"event-management".state_change_events`)
	assert.Contains(t, msg, "pg_stat_activity")
	assert.Contains(t, msg, "continues from here on the next start")

	// By value: everything before the busy table's first index is gone, it and
	// everything after it is still there.
	firstBlocked := -1
	for i, idx := range indexTrimDropped {
		if idx.table == "state_change_events" {
			firstBlocked = i
			break
		}
	}
	require.Equal(t, 4, firstBlocked, "the busy table's first index is the fifth entry")
	for i, idx := range indexTrimDropped {
		_, present := hypertableIndexes(t, sys, idx.table)[idx.name]
		assert.Equalf(t, i >= firstBlocked, present, "%s present after the failed run", idx.name)
	}

	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)
	require.NoError(t, newIndexTrimSchema(timing).Migrate(sys), "the next run must resume and finish")
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, keptIndexes[table], hypertableIndexes(t, sys, table), "indexes on %s", table)
	}

	// Neither timeout outlives its transaction on the session that ran the drops.
	for _, setting := range []string{"lock_timeout", "statement_timeout"} {
		var v string
		require.NoError(t, sys.Raw("SHOW "+setting).Scan(&v).Error)
		assert.Equalf(t, "0", v, "%s leaked out of the drop's transaction", setting)
	}
}

// TestIntegrationIndexTrimBoundsAnAttemptWhenChunksAreHeld covers the case lock_timeout
// alone does not: other sessions hold CHUNKS (not the hypertable) and each lets go just
// before lock_timeout would fire. Each single wait is then under lock_timeout, but a
// DROP holding the hypertable would add them up — and every write to the table waits
// on the hypertable for the sum. statement_timeout bounds the whole attempt.
func TestIntegrationIndexTrimBoundsAnAttemptWhenChunksAreHeld(t *testing.T) {
	timing := indexTrimTiming{lockTimeout: 500 * time.Millisecond, statementTimeout: 700 * time.Millisecond,
		pause: 100 * time.Millisecond, budget: time.Second}
	inst := freshInstance(t, "ittrimchunk")
	mgr := newPostgresManagerWith(t, inst, Migrations[:len(Migrations)-1])
	sys := systemDB(mgr)
	// Four weekly stamps: four chunks of every table.
	seedEventStore(t, sys, []string{"acme"}, 1, 4, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), 7*24*time.Hour)

	var chunks []string
	require.NoError(t, sys.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
		FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = 'alert_events'
		ORDER BY range_start`).Scan(&chunks).Error)
	require.Len(t, chunks, 4)

	// Three holders, one per chunk, releasing 400 ms apart: every single wait is under
	// lock_timeout, and together they span 1.2 s.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		conn := connectInstance(t, inst)
		_, err := conn.Exec(context.Background(), `BEGIN; LOCK TABLE `+chunks[i]+` IN ACCESS SHARE MODE`)
		require.NoError(t, err)
		wg.Add(1)
		go func(i int, conn *pgx.Conn) {
			defer wg.Done()
			time.Sleep(time.Duration(i+1) * 400 * time.Millisecond)
			_, _ = conn.Exec(context.Background(), `ROLLBACK`)
		}(i, conn)
	}
	defer wg.Wait()

	writer := connectInstance(t, inst)
	observer := connectInstance(t, inst)
	idx := indexTrimDropped[0]
	require.Equal(t, "alert_events", idx.table)

	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		// deadline = now: one attempt, no retry.
		err := dropIndexBounded(sys, idx, timing, time.Now())
		done <- result{err, time.Since(start)}
	}()

	// A write into the FOURTH chunk — which no holder touches — still has to pass the
	// hypertable the DROP holds, so it waits for the attempt; that wait must be bounded.
	waitForLockWaiter(t, observer, 2*time.Second)
	wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := writer.Exec(wctx, `INSERT INTO "event-management".alert_events
		(tenant_id, event_id, payload_id, device_token, event_type, occurred_time, type, level, message, source)
		VALUES ('acme', '\x02', '\x02', 'dev-9', 3, '2026-08-22T13:00:00Z', 't', 1, 'm', 's')`)
	cancel()
	require.NoError(t, err)
	writeDone := time.Since(start)

	r := <-done
	require.Error(t, r.err, "the attempt must end before the holders let go of every chunk")
	assert.True(t, isRetryableDropFailure(r.err), "the attempt must end as a busy table: %v", r.err)
	assert.Less(t, r.elapsed, timing.statementTimeout+300*time.Millisecond, "one attempt must be bounded")
	assert.Less(t, writeDone, timing.statementTimeout+400*time.Millisecond,
		"a write to the table must wait at most one bounded attempt")
}

// explainScans EXPLAINs one statement with sequential and bitmap scans disabled — so the
// plan names the index the planner prefers among the indexes, which is the question —
// and returns its relation scans. Everything runs in a transaction that is rolled back,
// so a negative control's DROP INDEX inside prepare never lands.
func explainScans(t *testing.T, db *gorm.DB, prepare []string, stmt string, args ...any) []planScan {
	t.Helper()
	var scans []planScan
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, s := range append([]string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_bitmapscan = off`}, prepare...) {
			if err := tx.Exec(s).Error; err != nil {
				return err
			}
		}
		var plan string
		if err := tx.Raw(`EXPLAIN (FORMAT JSON) `+stmt, args...).Row().Scan(&plan); err != nil {
			return err
		}
		var err error
		scans, err = planIndexScans([]byte(plan))
		if err != nil {
			return err
		}
		return errRollbackExplain
	})
	require.ErrorIs(t, err, errRollbackExplain)
	return scans
}

var errRollbackExplain = fmt.Errorf("rollback after explain")

// scansOf keeps the scans of one hypertable's chunks (and the hypertable itself).
func scansOf(t *testing.T, db *gorm.DB, scans []planScan, table string) []planScan {
	t.Helper()
	var chunkNames []string
	require.NoError(t, db.Raw(`SELECT chunk_name FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = ?`, table).Scan(&chunkNames).Error)
	own := map[string]bool{table: true}
	for _, c := range chunkNames {
		own[c] = true
	}
	var out []planScan
	for _, s := range scans {
		if own[s.relation] {
			out = append(out, s)
		}
	}
	return out
}

// requireServedBy asserts a plan scans table's chunks, and only through wantIndex.
func requireServedBy(t *testing.T, db *gorm.DB, scans []planScan, table, wantIndex, what string) {
	t.Helper()
	own := scansOf(t, db, scans, table)
	require.NotEmptyf(t, own, "%s: the plan scans no chunk of %s — nothing to check (%v)", what, table, scans)
	for _, s := range own {
		assert.Truef(t, chunkIndexIs(s.index, wantIndex), "%s: %s on %s uses %q, want %s",
			what, s.node, s.relation, s.index, wantIndex)
	}
}

// capture returns the recorded statement that contains want (the data query's ORDER BY,
// or the COUNT), failing if there is not exactly one.
func capture(t *testing.T, c *rdbtest.StatementCounter, want string) string {
	t.Helper()
	var hits []string
	for _, s := range c.MarkedSQL() {
		if strings.Contains(s, want) {
			hits = append(hits, s)
		}
	}
	require.Lenf(t, hits, 1, "statements containing %q: %v", want, c.MarkedSQL())
	return hits[0]
}

// TestIntegrationKeptReadsAreServedByTheirIndexes EXPLAINs the reads whose serving index
// the trim either removed a sibling of or relies on, over a seed large enough for the
// planner to prefer indexes, and asserts each is served by the index the migration's
// doc comment names for it.
//
// These are GUARDS against a later change, not the regression test for this one: most
// pass before the trim too (see the PR notes for which do not). They plan the SQL gorm
// rendered, with literals inlined — a custom plan. Production binds parameters and may
// switch to a generic plan after five executions, so the device read and its COUNT are
// also planned generically.
func TestIntegrationKeptReadsAreServedByTheirIndexes(t *testing.T) {
	inst := freshInstance(t, "ittrimplan")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	const perDevice = 50
	seedEventStore(t, sys, []string{"acme", "globex"}, 40, perDevice,
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), 19*24*time.Hour/perDevice)
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`ANALYZE "event-management".`+table).Error)
	}

	counter := rdbtest.NewStatementCounter(`"event-management"`)
	counter.Record(true)
	mgr.Database = mgr.Database.Session(&gorm.Session{Logger: counter})
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	device := "dev-7"
	page := rdb.Pagination{PageNumber: 1, PageSize: 5}

	// The device read, its COUNT, and the device + rare-type read.
	counter.Reset()
	res, err := api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device})
	require.NoError(t, err)
	require.Len(t, res.Results, 5)
	assert.EqualValues(t, perDevice, res.Pagination.TotalRecords)
	for i, ev := range res.Results {
		assert.Equal(t, device, ev.DeviceToken)
		if i > 0 {
			assert.True(t, ev.OccurredTime.Before(res.Results[i-1].OccurredTime), "newest first")
		}
	}
	deviceData := capture(t, counter, "ORDER BY")
	deviceCount := capture(t, counter, "count(*)")
	requireServedBy(t, sys, explainScans(t, sys, nil, deviceData), "events",
		"events_device_token_occurred_time_idx", "device read")
	requireServedBy(t, sys, explainScans(t, sys, nil, deviceCount), "events",
		"events_device_token_occurred_time_idx", "device read COUNT")

	counter.Reset()
	res, err = api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device,
		EventTypes: []esmodel.EventType{esmodel.Alert}})
	require.NoError(t, err)
	require.Len(t, res.Results, 1, "one alert per device in the seed")
	assert.Equal(t, esmodel.Alert, res.Results[0].EventType)
	requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "ORDER BY")), "events",
		"events_device_token_occurred_time_idx", "device + type read")
	requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "count(*)")), "events",
		"events_device_token_occurred_time_idx", "device + type COUNT")

	// The same device read and COUNT under a GENERIC plan — the plan a statement run
	// repeatedly with bound parameters can switch to — via EXPLAIN (GENERIC_PLAN), on
	// a raw connection's simple-query path so the $n placeholders reach the server
	// unbound.
	generic := connectInstance(t, inst)
	for _, s := range []string{`SET enable_seqscan = off`, `SET enable_bitmapscan = off`} {
		_, err := generic.Exec(context.Background(), s)
		require.NoError(t, err)
	}
	for what, sql := range map[string]string{"device read (generic)": deviceData, "device COUNT (generic)": deviceCount} {
		param := strings.ReplaceAll(strings.ReplaceAll(sql, "'"+device+"'", "$1"), "'acme'", "$2")
		require.Contains(t, param, "$1")
		require.Contains(t, param, "$2")
		results, err := generic.PgConn().Exec(context.Background(), `EXPLAIN (FORMAT JSON, GENERIC_PLAN) `+param).ReadAll()
		require.NoError(t, err, what)
		require.Len(t, results, 1)
		require.Len(t, results[0].Rows, 1)
		scans, err := planIndexScans(results[0].Rows[0][0])
		require.NoError(t, err)
		requireServedBy(t, sys, scans, "events", "events_device_token_occurred_time_idx", what)
	}

	// Negative control: the verdict is not vacuous. With the expected index gone the
	// same plan names something else, and the check says so.
	scans := explainScans(t, sys, []string{`DROP INDEX "event-management".events_device_token_occurred_time_idx`}, deviceData)
	own := scansOf(t, sys, scans, "events")
	require.NotEmpty(t, own)
	for _, s := range own {
		assert.False(t, chunkIndexIs(s.index, "events_device_token_occurred_time_idx"), s.index)
	}

	// Tenant-wide reads.
	for what, read := range map[string]struct {
		table, index string
		call         func() error
	}{
		"events tenant read": {"events", "events_tenant_id_occurred_time_idx", func() error {
			r, err := api.Events(ctx, EventSearchCriteria{Pagination: page})
			if err == nil && len(r.Results) != 5 {
				err = fmt.Errorf("got %d rows", len(r.Results))
			}
			return err
		}},
		"alert tenant read": {"alert_events", "alert_events_tenant_id_occurred_time_idx", func() error {
			r, err := api.AlertEvents(ctx, EventSearchCriteria{Pagination: page})
			if err == nil && len(r.Results) != 5 {
				err = fmt.Errorf("got %d rows", len(r.Results))
			}
			return err
		}},
		"location tenant read": {"location_events", "location_events_tenant_id_occurred_time_idx", func() error {
			r, err := api.LocationEvents(ctx, EventSearchCriteria{Pagination: page})
			if err == nil && len(r.Results) != 5 {
				err = fmt.Errorf("got %d rows", len(r.Results))
			}
			return err
		}},
	} {
		counter.Reset()
		require.NoError(t, read.call(), what)
		requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "ORDER BY")), read.table, read.index, what)
	}

	// The measurement_rollups refresh's cross-tenant range: the reason
	// measurement_events keeps its own occurred_time index.
	requireServedBy(t, sys, explainScans(t, sys, nil,
		`SELECT count(*) FROM "event-management".measurement_events
		 WHERE occurred_time >= '2026-08-10T00:00:00Z' AND occurred_time < '2026-08-10T06:00:00Z'`),
		"measurement_events", "measurement_events_occurred_time_idx", "rollup refresh range")

	// AnchorsForEvent, by the event's identity.
	counter.Reset()
	var one Event
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", device).Order("occurred_time").First(&one).Error)
	anchors, err := api.AnchorsForEvent(ctx, one.EventId, one.OccurredTime)
	require.NoError(t, err)
	require.Len(t, anchors, 1)
	assert.Equal(t, "area-3", anchors[0].AnchorToken)
	requireServedBy(t, sys, explainScans(t, sys, nil,
		`SELECT * FROM "event-management".event_anchors WHERE event_id = ? AND occurred_time = ? AND tenant_id = 'acme'`,
		one.EventId, one.OccurredTime), "event_anchors", "uq_event_anchors_idem", "anchors of one event")

	// The tenant purge's delete on the one table left with no other index.
	scans = explainScans(t, sys, nil, `DELETE FROM "event-management".state_change_events WHERE tenant_id = 'acme'`)
	requireServedBy(t, sys, scans, "state_change_events", "uq_state_change_events_idem", "tenant purge")
	for _, s := range scansOf(t, sys, scans, "state_change_events") {
		assert.Contains(t, s.indexCond, "tenant_id", "the purge must seek on tenant_id, not scan the index")
	}
}
