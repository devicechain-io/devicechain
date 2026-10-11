// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the tenant-led per-device event index (NewTenantDeviceIndexSchema),
// against a real TimescaleDB. Run as postgres_integration_test.go describes.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func tdiID() string { return NewTenantDeviceIndexSchema().ID }

// eventStoreIndexes is every index the six event hypertables carry at the end of the chain:
// the key rebuild's set with the per-device index led by the tenant.
var eventStoreIndexes = func() map[string]map[string]string {
	out := map[string]map[string]string{}
	for table, idx := range afterRekeyIndexes {
		out[table] = idx
	}
	out["events"] = map[string]string{
		"events_pkey":                   afterRekeyIndexes["events"]["events_pkey"],
		"idx_events_tenant_device_time": tenantDeviceIndex.def,
		"idx_events_tenant_alt_id":      afterRekeyIndexes["events"]["idx_events_tenant_alt_id"],
	}
	return out
}()

// storeIndexesFor is eventStoreIndexes as the database in hand should carry it: the chain
// through the per-device index leaves the tenant-wide alternate-id key, and the device-scoped
// migration after it swaps that one index for the device-scoped key. Both are the final set
// of the migration under test, so the check follows whichever the database has reached.
func storeIndexesFor(t *testing.T, db *gorm.DB) map[string]map[string]string {
	t.Helper()
	if !hasIndex(t, db, deviceAltIdKey.name) {
		return eventStoreIndexes
	}
	out := map[string]map[string]string{}
	for table, idx := range eventStoreIndexes {
		out[table] = idx
	}
	events := map[string]string{}
	for name, def := range eventStoreIndexes["events"] {
		if name != deviceAltIdKey.replaces {
			events[name] = def
		}
	}
	events[deviceAltIdKey.name] = newAltIdIndexDef
	out["events"] = events
	return out
}

// assertFinalIndexes asserts every event hypertable, and every chunk of it, carries exactly
// the final set.
func assertFinalIndexes(t *testing.T, db *gorm.DB, minChunks int) {
	t.Helper()
	want := storeIndexesFor(t, db)
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, want[table], hypertableIndexes(t, db, table), "indexes on %s", table)
	}
	assertChunkSets(t, db, minChunks, func(table string) []string { return sortedNames(want[table]) })
}

// afterRekey is a fresh database at the schema v0.19.0 shipped: the chain through the key
// rebuild, which this migration follows.
func afterRekey(t *testing.T, prefix string) (string, *rdb.RdbManager, *gorm.DB) {
	t.Helper()
	inst := freshInstance(t, prefix)
	mgr := newPostgresManagerWith(t, inst, migrationsThrough(t, rekeyID()))
	return inst, mgr, systemDB(mgr)
}

func deviceIndexOID(t *testing.T, db *gorm.DB, name string) uint32 {
	t.Helper()
	var oid uint32
	require.NoError(t, db.Raw(`SELECT ?::regclass::oid`, `"event-management".`+name).Scan(&oid).Error)
	return oid
}

func hasIndex(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	_, ok := hypertableIndexes(t, db, "events")[name]
	return ok
}

// holdWriter opens a transaction that has inserted into events: it holds ROW EXCLUSIVE on
// the table, which conflicts with the build's SHARE lock and NOT with the history count's
// ACCESS SHARE on the chunks. Returned: the release function.
func holdWriter(t *testing.T, inst string) func() {
	t.Helper()
	conn := connectInstance(t, inst)
	_, err := conn.Exec(context.Background(), `BEGIN`)
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), `INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source)
		VALUES ('acme', '\x0a0b', 'dev-held', 1, '2026-08-01T12:00:00Z', 'mqtt')`)
	require.NoError(t, err)
	return func() {
		_, err := conn.Exec(context.Background(), `ROLLBACK`)
		require.NoError(t, err)
	}
}

// TestIntegrationEventsDeviceIndexLeadsWithTenant is the by-value test of the change: after
// the real chain, events — the table and every chunk of it, compressed ones included — carries
// the tenant-led device index and not the tenant-less one, and every other table is untouched.
// On main events has events_device_token_occurred_time_idx and lacks idx_events_tenant_device_time,
// on the table and on every chunk.
func TestIntegrationEventsDeviceIndexLeadsWithTenant(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "ittdi"))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	assertFinalIndexes(t, sys, 2)

	// Negative control: the reader reads.
	require.NoError(t, sys.Exec(`CREATE INDEX tdi_probe ON "event-management".events (source)`).Error)
	assert.NotEqual(t, eventStoreIndexes["events"], hypertableIndexes(t, sys, "events"),
		"the index reader must see an index outside the final set")
	require.NoError(t, sys.Exec(`DROP INDEX "event-management".tdi_probe`).Error)

	// And over the shape every long-lived instance has: older chunks compressed before the
	// migration runs. The build locks and indexes through the compressed chunks, the device
	// read still returns every row newest first, and decompressing keeps the chunk sets final.
	inst, _, sysOld := afterRekey(t, "ittdiz")
	twoChunkSeed(t, sysOld)
	oldChunk := compressOldest(t, sysOld)
	after := newPostgresManagerWith(t, inst, Migrations)
	sys = systemDB(after)
	assertFinalIndexes(t, sys, 2)

	api := NewApi(after)
	ctx := core.WithTenant(context.Background(), "acme")
	d0 := "dev-0"
	byDevice, err := api.Events(ctx, EventSearchCriteria{DeviceToken: &d0})
	require.NoError(t, err)
	require.Len(t, byDevice.Results, 2)
	assert.True(t, byDevice.Results[0].OccurredTime.Equal(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)))
	assert.True(t, byDevice.Results[1].OccurredTime.Equal(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)))
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, oldChunk[table]).Error)
	}
	assertFinalIndexes(t, sys, 2)
}

// planNode is one relation scan of an EXPLAIN (ANALYZE, FORMAT JSON) plan, with what it
// actually did: rows it returned and rows it read and then discarded by a filter, both
// summed over its loops.
type planNode struct {
	node, relation, index, indexCond string
	rows, removedByFilter            int64
}

func walkAnalyzedPlan(t *testing.T, raw []byte) []planNode {
	t.Helper()
	var top []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &top))
	require.NotEmpty(t, top)
	var out []planNode
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		str := func(k string) string { v, _ := n[k].(string); return v }
		num := func(k string) int64 { v, _ := n[k].(float64); return int64(v) }
		nodeType := str("Node Type")
		if rel := str("Relation Name"); rel != "" && strings.HasSuffix(nodeType, "Scan") && nodeType != "Custom Scan" {
			loops := num("Actual Loops")
			out = append(out, planNode{node: nodeType, relation: rel, index: str("Index Name"),
				indexCond: str("Index Cond"), rows: num("Actual Rows") * loops,
				removedByFilter: num("Rows Removed by Filter") * loops})
		}
		if kids, ok := n["Plans"].([]any); ok {
			for _, k := range kids {
				if m, ok := k.(map[string]any); ok {
					walk(m)
				}
			}
		}
	}
	walk(top[0].Plan)
	return out
}

// explainAnalyzeEvents runs stmt under EXPLAIN (ANALYZE) in a rolled-back transaction, with
// sequential and bitmap scans off so the plan names the index the planner prefers, after
// prepare, and returns the scans of events' chunks. It requires at least one: a sum over
// nothing is not a measurement.
func explainAnalyzeEvents(t *testing.T, db *gorm.DB, prepare []string, stmt string) []planNode {
	t.Helper()
	var all []planNode
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, s := range append([]string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_bitmapscan = off`}, prepare...) {
			if err := tx.Exec(s).Error; err != nil {
				return err
			}
		}
		var plan string
		if err := tx.Raw(`EXPLAIN (ANALYZE, FORMAT JSON) ` + stmt).Row().Scan(&plan); err != nil {
			return err
		}
		all = walkAnalyzedPlan(t, []byte(plan))
		return errRollbackExplain
	})
	require.ErrorIs(t, err, errRollbackExplain)
	var chunkNames []string
	require.NoError(t, db.Raw(`SELECT chunk_name FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = 'events'`).Scan(&chunkNames).Error)
	own := map[string]bool{"events": true}
	for _, c := range chunkNames {
		own[c] = true
	}
	var out []planNode
	for _, n := range all {
		if own[n.relation] {
			out = append(out, n)
		}
	}
	require.NotEmptyf(t, out, "the plan scans no chunk of events — nothing to measure: %v", all)
	return out
}

func sumRemoved(nodes []planNode) (removed, rows int64) {
	for _, n := range nodes {
		removed += n.removedByFilter
		rows += n.rows
	}
	return
}

// TestIntegrationDeviceReadIsSeekedByTenant is the defect's own observable, by value: tenant
// globex has a busy device "gateway-1" and tenant acme a quiet device of the same token, and
// acme's device page and its COUNT must visit only acme's rows. Planned from the SQL the real
// reader issues. On main both are served by the tenant-less index with tenant_id a per-row
// filter, and the filter discards globex's rows.
func TestIntegrationDeviceReadIsSeekedByTenant(t *testing.T) {
	inst := freshInstance(t, "ittdiread")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	seedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 500 devices of 200 events a tenant, so no one token is a large share of the table: the
	// planner here prefers the device index for the page too. (With a token
	// that is a quarter of the table it expects to meet five of its rows within the first
	// twenty of the key and walks the tenant's rows instead; that is the planner's estimate,
	// not this index.)
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source)
		SELECT tn, sha256(convert_to(tn || ':' || d::text || ':' || g::text, 'UTF8')), 'dev-' || d::text, ?::bigint,
		       ?::timestamptz + g * interval '95 minutes', 'mqtt'
		FROM unnest(ARRAY['acme', 'globex']) tn, generate_series(0, 499) d, generate_series(0, 199) g`,
		int(esmodel.Measurement), seedStart).Error)

	// gateway-1: 2,000 rows for globex, 5 for acme (the last an alert), spread over the seed.
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source)
		SELECT 'globex', sha256(convert_to('globex:gw:' || g::text, 'UTF8')), 'gateway-1', ?::bigint,
		       ?::timestamptz + g * interval '10 minutes' + interval '1 second', 'mqtt'
		FROM generate_series(0, 1999) g`, int(esmodel.Measurement), seedStart).Error)
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source)
		SELECT 'acme', sha256(convert_to('acme:gw:' || g::text, 'UTF8')), 'gateway-1',
		       CASE WHEN g = 4 THEN ?::bigint ELSE ?::bigint END, ?::timestamptz + g * interval '3 hours', 'mqtt'
		FROM generate_series(0, 4) g`, int(esmodel.Alert), int(esmodel.Measurement), seedStart).Error)
	require.NoError(t, sys.Exec(`ANALYZE "event-management".events`).Error)

	counter := rdbtest.NewStatementCounter(`"event-management"`)
	counter.Record(true)
	mgr.Database = mgr.Database.Session(&gorm.Session{Logger: counter})
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	device := "gateway-1"
	page := rdb.Pagination{PageNumber: 1, PageSize: 5}

	counter.Reset()
	res, err := api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device})
	require.NoError(t, err)
	require.Len(t, res.Results, 5)
	assert.EqualValues(t, 5, res.Pagination.TotalRecords, "acme's gateway-1 only")
	_ = capture(t, counter, "ORDER BY") // exactly one data statement
	count := capture(t, counter, "count(*)")

	counter.Reset()
	typed, err := api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device,
		EventTypes: []esmodel.EventType{esmodel.Alert}})
	require.NoError(t, err)
	require.Len(t, typed.Results, 1)
	typedData := capture(t, counter, "ORDER BY")

	// visited = rows returned + rows read and then discarded by a filter. Acme has exactly 5
	// rows of gateway-1, so a read that visits only its own tenant's rows visits 5 and no more;
	// the type filter may discard acme's own other rows, never globex's.
	seek := func(what, stmt string, wantRemoved int64) {
		t.Helper()
		nodes := explainAnalyzeEvents(t, sys, nil, stmt)
		for _, n := range nodes {
			assert.Truef(t, chunkIndexIs(n.index, "idx_events_tenant_device_time"), "%s: %s on %s uses %q",
				what, n.node, n.relation, n.index)
			assert.Containsf(t, n.indexCond, "tenant_id", "%s: the tenant is an index condition (%s)", what, n.indexCond)
			assert.Containsf(t, n.indexCond, "device_token", "%s: the device is an index condition (%s)", what, n.indexCond)
		}
		removed, rows := sumRemoved(nodes)
		assert.Equalf(t, wantRemoved, removed, "%s: rows read and then discarded by a filter", what)
		assert.EqualValuesf(t, 5, removed+rows, "%s: rows visited are acme's rows of gateway-1, no one else's", what)
	}
	// The device PAGE (no type filter) is deliberately not asserted here: it has an ORDER BY
	// the events key already gives, and the planner walks that key with the device as a
	// filter, in this test's seed, as on the old index shape (a statistics estimate, not an
	// index choice; the reads test's seed plans it on the new index). What this change fixes is the COUNT and the type-filtered
	// read, which the tenant-less index could only answer by visiting other tenants' rows.
	seek("device COUNT", count, 0)
	seek("device + type page", typedData, 4)

	// Negative control: with the OLD index shape the same measurement sees globex's rows
	// discarded by the filter and no tenant in the index condition — so the zero above is
	// the index, not a check that cannot see.
	old := []string{`DROP INDEX "event-management".idx_events_tenant_device_time`,
		`CREATE INDEX events_device_token_occurred_time_idx ON "event-management".events (device_token, occurred_time DESC)`}
	for what, stmt := range map[string]string{"COUNT": count, "type-filtered page": typedData} {
		nodes := explainAnalyzeEvents(t, sys, old, stmt)
		removed, rows := sumRemoved(nodes)
		assert.Greaterf(t, removed+rows, int64(5), "on the tenant-less index the %s visits the other tenant's rows", what)
		for _, n := range nodes {
			assert.NotContains(t, n.indexCond, "tenant_id", "the old index cannot seek on the tenant")
		}
	}

	// Planned generically, as a prepared statement is after five executions.
	generic := connectInstance(t, inst)
	for _, s := range []string{`SET enable_seqscan = off`, `SET enable_bitmapscan = off`} {
		_, err := generic.Exec(context.Background(), s)
		require.NoError(t, err)
	}
	for what, sql := range map[string]string{"device + type read (generic)": typedData, "device COUNT (generic)": count} {
		param := strings.ReplaceAll(strings.ReplaceAll(sql, "'"+device+"'", "$1"), "'acme'", "$2")
		require.Contains(t, param, "$1")
		require.Contains(t, param, "$2")
		results, err := generic.PgConn().Exec(context.Background(), `EXPLAIN (FORMAT JSON, GENERIC_PLAN) `+param).ReadAll()
		require.NoError(t, err, what)
		require.Len(t, results, 1)
		require.Len(t, results[0].Rows, 1)
		scans, err := planIndexScans(results[0].Rows[0][0])
		require.NoError(t, err)
		requireServedBy(t, sys, scans, "events", "idx_events_tenant_device_time", what)
		for _, s := range scansOf(t, sys, scans, "events") {
			assert.Contains(t, s.indexCond, "tenant_id", what)
		}
	}

	// Counterweight: the order is unchanged. Two more acme events on the device at ONE
	// timestamp, distinct event ids, and the history comes back newest first with the
	// event_id tiebreak, acme's rows only.
	tied := seedStart.Add(30 * time.Hour)
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source) VALUES
		('acme', '\x01', 'gateway-1', ?::bigint, ?, 'mqtt'), ('acme', '\x02', 'gateway-1', ?::bigint, ?, 'mqtt')`,
		int(esmodel.Measurement), tied, int(esmodel.Measurement), tied).Error)
	all, err := api.Events(ctx, EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 50},
		DeviceToken: &device})
	require.NoError(t, err)
	require.Len(t, all.Results, 7)
	assert.EqualValues(t, 7, all.Pagination.TotalRecords)
	tiedAt := -1
	for i, ev := range all.Results {
		if i > 0 {
			assert.Falsef(t, ev.OccurredTime.After(all.Results[i-1].OccurredTime), "newest first at %d", i)
		}
		if ev.OccurredTime.Equal(tied) && tiedAt < 0 {
			tiedAt = i
		}
	}
	require.GreaterOrEqual(t, tiedAt, 0)
	require.True(t, all.Results[tiedAt+1].OccurredTime.Equal(tied))
	assert.Positive(t, bytes.Compare(all.Results[tiedAt].EventId, all.Results[tiedAt+1].EventId),
		"within one timestamp, event_id DESC")
	alerts, err := api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device,
		EventTypes: []esmodel.EventType{esmodel.Alert}})
	require.NoError(t, err)
	require.Len(t, alerts.Results, 1)
	assert.EqualValues(t, 1, alerts.Pagination.TotalRecords)
}

// TestIntegrationAnalyticsDeviceQueryUsesTheTenantLedIndex: a BI reader's device-filtered
// query, through the tenant-scoped view, is served by the new index with the tenant in the
// index condition. Its view predicate is tenant_id::text = reader_tenant(); this is the
// measurement that it becomes an index condition rather than a per-row filter.
func TestIntegrationAnalyticsDeviceQueryUsesTheTenantLedIndex(t *testing.T) {
	_, sys := analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)
	ctx := context.Background()
	for _, s := range []string{`SET enable_seqscan = off`, `SET enable_bitmapscan = off`} {
		_, err := conn.Exec(ctx, s)
		require.NoError(t, err)
	}
	var plan []byte
	require.NoError(t, conn.QueryRow(ctx,
		`EXPLAIN (FORMAT JSON) SELECT * FROM analytics.events WHERE device_token = 'device-1'`).Scan(&plan))
	scans, err := planIndexScans(plan)
	require.NoError(t, err)
	requireServedBy(t, sys, scans, "events", "idx_events_tenant_device_time", "BI device query")
	for _, s := range scansOf(t, sys, scans, "events") {
		assert.Contains(t, s.indexCond, "tenant_id", "the reader's tenant is an index condition")
		assert.Contains(t, s.indexCond, "device_token")
	}
}

// TestIntegrationDeviceIndexBuildLetsReadsThrough holds the build at the moment its lock is
// held (the afterDeviceIndexLock seam) and proves the lock is SHARE, not ACCESS EXCLUSIVE:
// a read of events completes at once, and a write waits.
func TestIntegrationDeviceIndexBuildLetsReadsThrough(t *testing.T) {
	inst, _, sys := afterRekey(t, "ittdishare")
	twoChunkSeed(t, sys)

	locked, release := make(chan struct{}), make(chan struct{})
	afterDeviceIndexLock = func() {
		close(locked)
		<-release
	}
	t.Cleanup(func() { afterDeviceIndexLock = nil })

	done := make(chan error, 1)
	go func() { done <- newTenantDeviceIndexSchema(testRekeyTiming).Migrate(sys) }()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("the migration ended without locking: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the build never locked the table")
	}

	var shareGranted bool
	observer := connectInstance(t, inst)
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT COALESCE(bool_or(granted), false) FROM pg_locks
		WHERE mode = 'ShareLock' AND relation = '"event-management".events'::regclass`).Scan(&shareGranted))
	assert.True(t, shareGranted, "the build holds a ShareLock on events")

	reader := connectInstance(t, inst)
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	start := time.Now()
	var n int
	require.NoError(t, reader.QueryRow(rctx, `SELECT count(*) FROM "event-management".events WHERE tenant_id = 'acme'`).Scan(&n),
		"a read of events must not wait for the build")
	cancel()
	assert.Equal(t, 4, n)
	assert.Less(t, time.Since(start), time.Second)

	writer := connectInstance(t, inst)
	wrote := make(chan error, 1)
	go func() {
		_, err := writer.Exec(context.Background(), `INSERT INTO "event-management".events
			(tenant_id, event_id, device_token, event_type, occurred_time, source)
			VALUES ('acme', '\x0c0d', 'dev-w', 1, '2026-08-20T13:00:00Z', 'mqtt')`)
		wrote <- err
	}()
	waitForLockWaiter(t, observer, 5*time.Second)
	select {
	case err := <-wrote:
		t.Fatalf("a write completed while the build held its lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-wrote, "the write completes once the build commits")
	assertFinalIndexes(t, sys, 2)
}

// TestIntegrationDeviceIndexWaitsBoundedlyForALockAndResumes: a session with an open write
// holds the table against the build's SHARE lock. The migration ends within its budget with
// the busy error and the holder query, having changed nothing (the old index stays, the new
// one does not exist); once released, the next run finishes.
func TestIntegrationDeviceIndexWaitsBoundedlyForALockAndResumes(t *testing.T) {
	inst, _, sys := afterRekey(t, "ittdibusy")
	twoChunkSeed(t, sys)
	timing := testRekeyTiming
	timing.budget = 8 * time.Second

	release := holdWriter(t, inst)
	start := time.Now()
	err := newTenantDeviceIndexSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stayed busy")
	assert.Contains(t, err.Error(), indexTrimHolderQuery("events"))
	assert.NotContains(t, err.Error(), "dcctl destroy")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the last server error stays wrapped: %v", err)
	assert.Equal(t, "55P03", pgErr.Code)
	assert.Less(t, elapsed, timing.budget+time.Second, "bounded by the budget")
	assert.Greater(t, elapsed, 2*timing.lockTimeout, "a busy table is retried, not given up on at once")

	// By value: nothing changed.
	assert.True(t, hasIndex(t, sys, tenantDeviceIndex.replaces), "the old index stays")
	assert.False(t, hasIndex(t, sys, tenantDeviceIndex.name), "the new index does not exist")

	release()
	require.NoError(t, newTenantDeviceIndexSchema(timing).Migrate(sys))
	assertFinalIndexes(t, sys, 2)
	for _, setting := range []string{"lock_timeout", "statement_timeout"} {
		var v string
		require.NoError(t, sys.Raw("SHOW "+setting).Scan(&v).Error)
		assert.Equalf(t, "0", v, "%s leaked out of its transaction", setting)
	}
}

// TestIntegrationDeviceIndexGatesRefuseBeforeLocking: the history gate and the chunk gate, at
// their boundaries. Each refuses at one below, changing nothing and taking no lock (the call
// returns at once while a writer holds ROW EXCLUSIVE on events, which a queued SHARE would
// wait behind), with the manual statements and no recreate; each builds at exactly the bound.
// The seed holds 4 rows in events and 4 in each other table, and events has 2 chunks:
// counting the other tables would tip the history gate.
func TestIntegrationDeviceIndexGatesRefuseBeforeLocking(t *testing.T) {
	inst, _, sys := afterRekey(t, "ittdigate")
	twoChunkSeed(t, sys)
	release := holdWriter(t, inst)

	timing := testRekeyTiming
	timing.maxRows = 3
	start := time.Now()
	err := newTenantDeviceIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"more than 3 rows", tenantDeviceIndexManualDrop, tenantDeviceIndexManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "dcctl destroy")
	assert.True(t, hasIndex(t, sys, tenantDeviceIndex.replaces))
	assert.False(t, hasIndex(t, sys, tenantDeviceIndex.name))

	timing = testRekeyTiming
	timing.maxChunks = 1
	start = time.Now()
	err = newTenantDeviceIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"events (2) has", "more than 1 chunks", tenantDeviceIndexManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "dcctl destroy")
	assert.False(t, hasIndex(t, sys, tenantDeviceIndex.name))
	release()

	timing = testRekeyTiming
	timing.maxRows, timing.maxChunks = 4, 2
	require.NoError(t, newTenantDeviceIndexSchema(timing).Migrate(sys), "exactly at both bounds builds")
	assertFinalIndexes(t, sys, 2)
}

// TestIntegrationDeviceIndexTooSlowIsRolledBackAndSticks: a build that runs out of its whole
// allowance is rolled back, decided on the first attempt, and leaves a marker on the OLD
// index; the next start refuses at once without locking; and the manual statements the
// message publishes, run verbatim, let the next start finish with no marker to clear.
func TestIntegrationDeviceIndexTooSlowIsRolledBackAndSticks(t *testing.T) {
	inst, _, sys := afterRekey(t, "ittdislow")
	// 200,000 events: indexing them takes far more than 50 ms.
	seedEventStore(t, sys, []string{"acme"}, 200, 1000, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Minute)

	timing := testRekeyTiming
	timing.tableBuild = 50 * time.Millisecond
	timing.pause = 2 * time.Second
	timing.lockAttempt = 2 * time.Second
	timing.maxRows = 10_000_000
	start := time.Now()
	err := newTenantDeviceIndexSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	for _, want := range []string{"did not finish within 50ms", tenantDeviceIndexManualDrop, tenantDeviceIndexManualBuild,
		`COMMENT ON INDEX "event-management".events_device_token_occurred_time_idx IS NULL`} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "dcctl destroy")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the server error stays wrapped: %v", err)
	assert.Equal(t, "57014", pgErr.Code)
	assert.Less(t, elapsed, timing.pause, "too slow is decided on the first attempt, never retried")

	assert.False(t, hasIndex(t, sys, tenantDeviceIndex.name), "rolled back whole")
	assert.True(t, hasIndex(t, sys, tenantDeviceIndex.replaces))
	var note string
	require.NoError(t, sys.Raw(`SELECT COALESCE(obj_description('"event-management".events_device_token_occurred_time_idx'::regclass, 'pg_class'), '')`).Scan(&note).Error)
	assert.True(t, strings.HasPrefix(note, tenantDeviceIndexTooSlowMarker), "the verdict is recorded: %q", note)

	// The next start refuses at once while another session holds events ACCESS EXCLUSIVE.
	holder := connectInstance(t, inst)
	_, err = holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	start = time.Now()
	err = newTenantDeviceIndexSchema(testRekeyTiming).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refused build is refused without waiting on any lock")
	assert.Contains(t, err.Error(), "an earlier start found it too slow")
	assert.NotContains(t, err.Error(), "dcctl destroy")
	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)

	// The published remedy, verbatim: the next start then finishes without clearing anything.
	require.NoError(t, sys.Exec(tenantDeviceIndexManualDrop).Error)
	require.NoError(t, sys.Exec(tenantDeviceIndexManualBuild).Error)
	require.NoError(t, newTenantDeviceIndexSchema(testRekeyTiming).Migrate(sys))
	assertFinalIndexes(t, sys, 1)

	// With the marker and no valid new index, clearing the comment lets a start build.
	_, _, sys2 := afterRekey(t, "ittdislow2")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(`COMMENT ON INDEX "event-management".events_device_token_occurred_time_idx IS '`+
		tenantDeviceIndexTooSlowMarker+`: test'`).Error)
	err = NewTenantDeviceIndexSchema().Migrate(sys2)
	require.Error(t, err)
	require.NoError(t, sys2.Exec(`COMMENT ON INDEX "event-management".events_device_token_occurred_time_idx IS NULL`).Error)
	require.NoError(t, NewTenantDeviceIndexSchema().Migrate(sys2), "cleared, the build runs again")
	assertFinalIndexes(t, sys2, 2)
}

// invalidate marks the new index INVALID, the state an interrupted per-chunk build leaves.
func invalidate(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`UPDATE pg_index SET indisvalid = false
		WHERE indexrelid = '"event-management".idx_events_tenant_device_time'::regclass`).Error)
}

// TestIntegrationDeviceIndexDropsAnInvalidLeftover: an invalid index of the new name is
// dropped and rebuilt (a different oid, valid, the old one gone). When a gate refuses first,
// the leftover is still there and the refusal hands over the DROP; the statements run
// verbatim let the next start finish.
func TestIntegrationDeviceIndexDropsAnInvalidLeftover(t *testing.T) {
	_, _, sys := afterRekey(t, "ittdileft")
	twoChunkSeed(t, sys)
	require.NoError(t, sys.Exec(tenantDeviceIndexManualBuild).Error)
	invalidate(t, sys)
	leftover := deviceIndexOID(t, sys, tenantDeviceIndex.name)

	// A refusal changes nothing: the leftover is still there, and the message says how to
	// remove it before building (IF NOT EXISTS would have skipped it as built).
	timing := testRekeyTiming
	timing.maxRows = 0
	err := newTenantDeviceIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), tenantDeviceIndexManualDrop)
	assert.Equal(t, leftover, deviceIndexOID(t, sys, tenantDeviceIndex.name), "a refusal leaves the leftover")
	assert.Error(t, sys.Exec(tenantDeviceIndexManualBuild).Error, "the build statement fails loudly over a leftover")

	// The remedy, verbatim; the next start, which would refuse any build, finishes.
	require.NoError(t, sys.Exec(tenantDeviceIndexManualDrop).Error)
	require.NoError(t, sys.Exec(tenantDeviceIndexManualBuild).Error)
	require.NoError(t, newTenantDeviceIndexSchema(timing).Migrate(sys))
	assertFinalIndexes(t, sys, 2)

	// And a start within its bounds replaces the leftover itself.
	_, _, sys2 := afterRekey(t, "ittdileft2")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(tenantDeviceIndexManualBuild).Error)
	invalidate(t, sys2)
	leftover = deviceIndexOID(t, sys2, tenantDeviceIndex.name)
	require.NoError(t, NewTenantDeviceIndexSchema().Migrate(sys2))
	assert.NotEqual(t, leftover, deviceIndexOID(t, sys2, tenantDeviceIndex.name), "the leftover was rebuilt")
	assertFinalIndexes(t, sys2, 2)
}

// TestIntegrationDeviceIndexRefusesAShapeItDidNotWrite: an index of the new name that is not
// the one this migration builds is refused, with nothing changed and the foreign index left.
func TestIntegrationDeviceIndexRefusesAShapeItDidNotWrite(t *testing.T) {
	_, _, sys := afterRekey(t, "ittdishape")
	twoChunkSeed(t, sys)
	for _, ddl := range []string{
		`CREATE INDEX idx_events_tenant_device_time ON "event-management".events (tenant_id, device_token)`,
		`CREATE UNIQUE INDEX idx_events_tenant_device_time ON "event-management".events (tenant_id, device_token, occurred_time DESC)`,
	} {
		require.NoError(t, sys.Exec(ddl).Error)
		foreign := hypertableIndexes(t, sys, "events")[tenantDeviceIndex.name]
		err := NewTenantDeviceIndexSchema().Migrate(sys)
		require.Error(t, err, ddl)
		assert.Contains(t, err.Error(), "not the index this migration builds")
		assert.True(t, hasIndex(t, sys, tenantDeviceIndex.replaces), "the old index stays")
		assert.Equal(t, foreign, hypertableIndexes(t, sys, "events")[tenantDeviceIndex.name], "the foreign index is untouched")
		require.NoError(t, sys.Exec(`DROP INDEX "event-management".idx_events_tenant_device_time`).Error)
	}
}

// TestIntegrationDeviceIndexRerunAndManualBuildBeforeUpgrade: a re-run on the migration's own
// output takes no lock and rebuilds nothing; an old index that comes back is dropped and the
// new one is not rebuilt; and an index built by hand before the upgrade makes the migration
// a drop-only start that ignores both bounds (a start the gates would refuse, finishes).
func TestIntegrationDeviceIndexRerunAndManualBuildBeforeUpgrade(t *testing.T) {
	inst := freshInstance(t, "ittdirerun")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	built := deviceIndexOID(t, sys, tenantDeviceIndex.name)

	holder := connectInstance(t, inst)
	_, err := holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, NewTenantDeviceIndexSchema().Migrate(sys))
	assert.Less(t, time.Since(start), time.Second, "a re-run takes no lock")
	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)
	assert.Equal(t, built, deviceIndexOID(t, sys, tenantDeviceIndex.name), "a re-run rebuilds nothing")

	require.NoError(t, sys.Exec(`CREATE INDEX events_device_token_occurred_time_idx
		ON "event-management".events (device_token, occurred_time DESC)`).Error)
	require.NoError(t, NewTenantDeviceIndexSchema().Migrate(sys))
	assert.Equal(t, built, deviceIndexOID(t, sys, tenantDeviceIndex.name), "only the old index is dropped")
	assertFinalIndexes(t, sys, 2)

	// Built by hand on the v0.19.0 schema, before upgrading.
	_, _, sys2 := afterRekey(t, "ittdimanual")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(tenantDeviceIndexManualBuild).Error)
	timing := testRekeyTiming
	timing.maxRows, timing.maxChunks = 0, 1
	require.NoError(t, newTenantDeviceIndexSchema(timing).Migrate(sys2), "drop-only: no gate applies")
	assertFinalIndexes(t, sys2, 2)
}
