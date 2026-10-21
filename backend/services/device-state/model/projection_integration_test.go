// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The projection's merge rules against a REAL PostgreSQL, through device-state's real
// migration chain. SQLite compares the time columns as text and keeps whatever precision it
// is handed; PostgreSQL compares timestamps and keeps microseconds — so the equivalence
// table and the precision rule are proven here too, not only on SQLite.
//
// Run with the integration tag against a server on DC_IT_PGHOST/DC_IT_PGPORT (user and
// password postgres unless DC_IT_PGUSER/DC_IT_PGPASSWORD say otherwise), as
// hack/integration-tests.sh does.
package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newPostgresProjectionApi migrates a database of its own named for the test and name, and
// returns an Api over it.
func newPostgresProjectionApi(t testing.TB, name string) *Api {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		t.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "postgres")
	instance := strings.ToLower(fmt.Sprintf("dsproj%d%s", time.Now().UnixNano()%1_000_000_000, name))
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		t.Fatalf("create the instance database: %v", err)
	}
	return openPostgresProjectionApi(t, host, port, user, pass, instance)
}

// newPostgresReplica is a second Api over the SAME database as api, through a connection pool of
// its own: what a second device-state replica is to the first.
func newPostgresReplica(t testing.TB, api *Api) *Api {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		t.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	return openPostgresProjectionApi(t, itEnv("DC_IT_PGHOST", "localhost"), port, itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "postgres"), api.RDB.Microservice.InstanceId)
}

// openPostgresProjectionApi migrates the instance database (a no-op when it already is) and
// returns an Api over a pool of its own.
func openPostgresProjectionApi(t testing.TB, host string, port int, user, pass, instance string) *Api {
	t.Helper()
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "device-state"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("run migrations on the real server: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return NewApi(mgr)
}

// truncateProjection empties the three tables, so one database serves many cases.
func truncateProjection(t testing.TB, api *Api) {
	t.Helper()
	if err := api.RDB.Database.Exec(`TRUNCATE "device-state".device_states, "device-state".latest_measurements, ` +
		`"device-state".latest_locations`).Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// The scripted table and 60 generated sequences, on PostgreSQL. Two databases serve every
// case, emptied between them.
func TestABatchLeavesWhatMergingOneAtATimeLeavesOnPostgres(t *testing.T) {
	a, b := newPostgresProjectionApi(t, "a"), newPostgresProjectionApi(t, "b")
	reuse := func(t *testing.T, name string) *Api {
		api := a
		if name == "b" {
			api = b
		}
		truncateProjection(t, api)
		return api
	}
	for _, c := range equivalenceCases() {
		t.Run(c.name, func(t *testing.T) { assertEquivalent(t, reuse, c) })
	}
	const seed = 20260927
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < 60; i++ {
		c := equivalenceCase{name: fmt.Sprintf("seed%d-seq%03d", seed, i), updates: randomUpdates(r)}
		if r.Intn(2) == 0 {
			c.seed = randomUpdates(r)
		}
		t.Run(c.name, func(t *testing.T) { assertEquivalent(t, reuse, c) })
	}
}

// The data-only generator and the swept seeds, on PostgreSQL: the heartbeat upsert's SQL against
// merging one at a time. 100 sequences; SQLite runs 200 of the same.
func TestADataOnlyBatchLeavesWhatMergingOneAtATimeLeavesOnPostgres(t *testing.T) {
	a, b := newPostgresProjectionApi(t, "a"), newPostgresProjectionApi(t, "b")
	reuse := func(t *testing.T, name string) *Api {
		api := a
		if name == "b" {
			api = b
		}
		truncateProjection(t, api)
		return api
	}
	const seed = 20261014
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < 100; i++ {
		c := dataOnlyCase(r, seed, i)
		t.Run(c.name, func(t *testing.T) { assertEquivalent(t, reuse, c) })
	}
}

// 🔴 THE DATABASE KEEPS MICROSECONDS, AND THE MERGE USED TO COMPARE NANOSECONDS. A reading at
// .0000013 is stored as .000001; a later, OLDER reading at .0000011 then compared as newer
// than what was stored and replaced it — the projection went backwards. Both readings are in
// one microsecond, so they are equal once stored, and the first stored stays.
func TestPostgresKeepsTheFirstOfTwoReadingsInOneMicrosecond(t *testing.T) {
	api := newPostgresProjectionApi(t, "usec")
	ctx := core.WithTenant(context.Background(), "acme")
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "temp", Value: num(10), OccurredTime: base.Add(1300 * time.Nanosecond)}}); err != nil {
		t.Fatalf("first reading: %v", err)
	}
	if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "temp", Value: num(11), OccurredTime: base.Add(1100 * time.Nanosecond)}}); err != nil {
		t.Fatalf("older reading: %v", err)
	}
	got := mustReading(t, api, ctx, "d1", "temp")
	if got.Value.Float64 != 10 {
		t.Errorf("value = %v; an older reading replaced the stored one", got.Value.Float64)
	}
	if !got.OccurredTime.Equal(base.Add(time.Microsecond)) {
		t.Errorf("stored time = %v; want %v", got.OccurredTime, base.Add(time.Microsecond))
	}
	// The same for positions.
	if err := api.MergeLatestLocations(ctx, "d1", []LatestLocationInput{
		{Latitude: num(28), OccurredTime: base.Add(1300 * time.Nanosecond)}}); err != nil {
		t.Fatalf("first fix: %v", err)
	}
	if err := api.MergeLatestLocations(ctx, "d1", []LatestLocationInput{
		{Latitude: num(29), OccurredTime: base.Add(1100 * time.Nanosecond)}}); err != nil {
		t.Fatalf("older fix: %v", err)
	}
	if loc := loadLocation(t, api, ctx, "d1"); loc.Latitude.Float64 != 28 {
		t.Errorf("latitude = %v; an older fix replaced the stored one", loc.Latitude.Float64)
	}
}

// theirRowPG is the row another writer's first event leaves for a device: data at t0, external
// id "theirs", written raw on the batch's own connection.
func theirRowPG(t *testing.T, tx *gorm.DB, token string, t0 time.Time) {
	t.Helper()
	if _, err := tx.Statement.ConnPool.ExecContext(context.Background(),
		`INSERT INTO "device-state".device_states (created_at, updated_at, tenant_id, device_token, external_id, `+
			`source, active, last_connect_time, last_activity_time, inactivity_timeout, presence_source, session_id) `+
			`VALUES ($1, $2, 'acme', $3, 'theirs', '', true, $4, $5, 600, 'INFERRED', 0)`,
		t0, t0, token, t0, t0); err != nil {
		t.Errorf("the other writer's insert: %v", err)
	}
}

func loadRacePG(t *testing.T, api *Api, token string) DeviceState {
	t.Helper()
	var rows []DeviceState
	if err := api.RDB.DB(core.WithTenant(context.Background(), "acme")).Where("device_token = ?", token).Find(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for %s; want exactly 1", len(rows), token)
	}
	return rows[0]
}

// A device's row created by another writer just before the batch takes its rows, on
// PostgreSQL: lockOrCreate takes the conflict arm (locking the row, inserting nothing), and the
// batch folds its events into the row that is there. This is the LOCKED path (a batch with a
// presence claim); the heartbeat path is the next test. The other writer is simulated inside
// the batch's own transaction, right before its first create, as the SQLite test does.
func TestABatchFoldsIntoARowAnotherWriterCreatedMeanwhileOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "race")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	armed := true
	if err := api.RDB.Database.Callback().Create().Before("gorm:create").Register("test:create_meanwhile", func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "device_states" {
			return
		}
		armed = false
		theirRowPG(t, tx, "race-1", t0)
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(time.Minute), Identity: DeviceIdentity{Source: "mqtt1"},
			Presence: &PresenceTransition{Claim: presence.ClaimConnected, SessionId: 1, OccurredAt: t0.Add(time.Minute)}},
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("MergeProjectionBatch over a row created meanwhile: %v", err)
	}
	if armed {
		t.Fatal("the callback never ran, so this test did not put a row in the gap")
	}
	if got := loadRacePG(t, api, "race-1"); got.ExternalId != "theirs" || got.Source != "mqtt1" ||
		got.PresenceSource != PresenceSourceAsserted || !got.LastActivityTime.Time.Equal(t0.Add(2*time.Minute)) ||
		!got.LastConnectTime.Time.Equal(t0.Add(time.Minute)) {
		t.Errorf("row = ext %q src %q %s activity %v connect %v; want theirs/mqtt1, ASSERTED, %v, %v", got.ExternalId,
			got.Source, got.PresenceSource, got.LastActivityTime.Time, got.LastConnectTime.Time, t0.Add(2*time.Minute), t0.Add(time.Minute))
	}
}

// The same race for a data-only batch, which takes the heartbeat upsert: the other writer's row
// lands right before the batch's INSERT (a callback creates it on the batch's own connection
// just before the create runs), and the INSERT takes its conflict arm and folds the batch into
// it, with no lock read.
func TestAHeartbeatBatchFoldsIntoARowAnotherWriterCreatedMeanwhileOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "hbrace")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	armed := true
	if err := api.RDB.Database.Callback().Create().Before("gorm:create").Register("test:create_meanwhile", func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "device_states" {
			return
		}
		armed = false
		theirRowPG(t, tx, "race-1", t0)
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	counter := rdbtest.NewStatementCounter("FOR UPDATE")
	counted := NewApi(&rdb.RdbManager{Database: api.RDB.Database.Session(&gorm.Session{Logger: counter})})
	counter.Reset()
	if err := counted.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(time.Minute), Identity: DeviceIdentity{Source: "mqtt1"}},
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("MergeProjectionBatch over a row created meanwhile: %v", err)
	}
	if armed {
		t.Fatal("the callback never ran, so this test did not put a row in the gap")
	}
	if _, locking := counter.Counts(); locking != 0 {
		t.Errorf("%d FOR UPDATE statements; a data-only batch takes none", locking)
	}
	if got := loadRacePG(t, api, "race-1"); got.ExternalId != "theirs" || got.Source != "mqtt1" ||
		!got.LastActivityTime.Time.Equal(t0.Add(2*time.Minute)) || !got.LastConnectTime.Time.Equal(t0) {
		t.Errorf("row = ext %q src %q activity %v connect %v; want theirs/mqtt1, %v, %v", got.ExternalId, got.Source,
			got.LastActivityTime.Time, got.LastConnectTime.Time, t0.Add(2*time.Minute), t0)
	}
}

// holdsRowUntilCommit runs batch against a device whose row exists, and fires a single-event
// merge for the same device from a connection of its own at the moment hook registers it for
// (right after the batch's lock read, or right after its upsert). The merge must still be waiting
// for the row when the batch moves on, and, once the batch commits, read the batch's row and move
// it forward: the device's final activity is the merge's.
func holdsRowUntilCommit(t *testing.T, name string, afterCreate bool, batch func(tok string, t0 time.Time) []ProjectionUpdate) {
	api := newPostgresProjectionApi(t, name)
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "lock-1", OccurredAt: t0},
	}); err != nil {
		t.Fatalf("seed the row: %v", err)
	}

	var armed atomic.Bool
	armed.Store(true)
	done := make(chan error, 1)
	var finishedInsideBatch bool
	fire := func(tx *gorm.DB) {
		if tx.Statement.Table != "device_states" || !armed.CompareAndSwap(true, false) {
			return
		}
		go func() {
			_, err := api.MergeDeviceState(ctx, "lock-1", t0.Add(5*time.Minute), nil, DeviceIdentity{})
			done <- err
		}()
		select {
		case err := <-done:
			finishedInsideBatch = true
			done <- err
		case <-time.After(2 * time.Second):
		}
	}
	if afterCreate {
		if err := api.RDB.Database.Callback().Create().After("gorm:create").Register("test:write_meanwhile", fire); err != nil {
			t.Fatalf("register callback: %v", err)
		}
	} else {
		if err := api.RDB.Database.Callback().Query().After("gorm:query").Register("test:write_meanwhile", fire); err != nil {
			t.Fatalf("register callback: %v", err)
		}
	}

	if err := api.MergeProjectionBatch(context.Background(), batch("lock-1", t0)); err != nil {
		t.Fatalf("MergeProjectionBatch: %v", err)
	}
	if armed.Load() {
		t.Fatal("the callback never ran, so no other writer met the batch")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the other writer's merge: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the other writer never finished")
	}
	if finishedInsideBatch {
		t.Error("the other writer committed while the batch still had the row it wrote: the batch does not lock it")
	}
	if got := loadRacePG(t, api, "lock-1").LastActivityTime.Time; !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("last activity = %v; want the other writer's newer %v — the batch wrote a stale copy over it",
			got, t0.Add(5*time.Minute))
	}
}

// 🔴 THE BATCH HOLDS EVERY ROW IT READS UNTIL IT COMMITS. The locked path (a batch with a
// presence claim) reads each existing row with SELECT … FOR UPDATE, folds the batch into its copy
// and writes it back. Without the lock that is an unlocked read-modify-write: another writer (here
// the single-event merge) commits a newer activity between the batch's read and its write-back, and
// the batch then writes its stale copy over it — the device's activity goes backwards. The other
// writer starts right after the batch's read, from a connection of its own.
func TestABatchHoldsTheRowsItReadUntilItCommitsOnPostgres(t *testing.T) {
	holdsRowUntilCommit(t, "lock", false, func(tok string, t0 time.Time) []ProjectionUpdate {
		return []ProjectionUpdate{claim("acme", tok, t0.Add(time.Minute), presence.ClaimConnected, 1)}
	})
}

// The heartbeat upsert holds the row too: ON CONFLICT DO UPDATE locks the row it updates, so a
// concurrent single-event merge, started right after the batch's upsert, waits for the batch's
// commit and moves the row forward from the batch's value.
func TestAHeartbeatBatchHoldsTheRowsItWroteUntilItCommitsOnPostgres(t *testing.T) {
	holdsRowUntilCommit(t, "hblock", true, func(tok string, t0 time.Time) []ProjectionUpdate {
		return []ProjectionUpdate{data("acme", tok, t0.Add(time.Minute))}
	})
}

// A data-only batch takes NO row lock with a SELECT: no FOR UPDATE statement at all, where main
// sent one lock read per tenant. A batch with a claim still sends exactly one. The statement is
// counted on a server that renders locking clauses (SQLite drops them), so this cannot be shown
// anywhere but here.
func TestADataOnlyBatchSendsNoForUpdateOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "noforupdate")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	counter := rdbtest.NewStatementCounter("FOR UPDATE")
	counted := NewApi(&rdb.RdbManager{Database: api.RDB.Database.Session(&gorm.Session{Logger: counter})})
	devices := func(when time.Time, withClaim bool) []ProjectionUpdate {
		var updates []ProjectionUpdate
		for i := 0; i < 8; i++ {
			updates = append(updates, reading("acme", fmt.Sprintf("nf-%d", i), when, "temp", float64(i)))
		}
		if withClaim {
			updates[0] = claim("acme", "nf-0", when, presence.ClaimConnected, 1)
		}
		return updates
	}
	for _, tc := range []struct {
		name      string
		when      time.Duration
		withClaim bool
		want      int64
	}{
		{"first sight, data only", 0, false, 0},
		{"devices seen before, data only", time.Minute, false, 0},
		{"devices seen before, one claim", 2 * time.Minute, true, 1},
	} {
		counter.Reset()
		if err := counted.MergeProjectionBatch(context.Background(), devices(t0.Add(tc.when), tc.withClaim)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, locking := counter.Counts(); locking != tc.want {
			t.Errorf("%s: %d FOR UPDATE statements; want %d", tc.name, locking, tc.want)
		}
	}
}

// TWO REPLICAS MERGING THE SAME DEVICES END WHERE ONE REPLICA DOES. Two Apis on two pools
// (what two device-state replicas are) each run 5 writers over 40 devices that start with NO
// row — so first sight is contended — sending batches of data events (the heartbeat upsert) and
// now and then one with a claim (the locked path) for a device. Event times are distinct and
// handed out in an order unrelated to commit order. No batch may fail (a deadlock between the two
// lock shapes would surface here as an error, and in production as a fallback to one event per
// transaction), and the projection must equal the same batches merged serially into a third
// database, on every field the order of arrival cannot change: activity, presence, session,
// identity, and every latest reading.
func TestTwoReplicasMergingTheSameDevicesEndInTheSerialState(t *testing.T) {
	a := newPostgresProjectionApi(t, "conc")
	b := newPostgresReplica(t, a)
	serial := newPostgresProjectionApi(t, "serial")

	const devices, writers, batchesPerWriter = 40, 5, 40
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	tokenOf := func(i int) string { return fmt.Sprintf("cc-%03d", i) }

	// Every writer's batches, generated up front so the serial run can replay them. Each batch
	// takes a sorted random subset of devices; one in eight carries a CONNECTED claim for its
	// first device, with a session that only ever rises.
	var plan [][][]ProjectionUpdate // plan[replica*writers+writer][batch]
	r := rand.New(rand.NewSource(20261014))
	var session atomic.Uint64
	for w := 0; w < 2*writers; w++ {
		var batches [][]ProjectionUpdate
		for n := 0; n < batchesPerWriter; n++ {
			size := 1 + r.Intn(12)
			picked := r.Perm(devices)[:size]
			sort.Ints(picked)
			var batch []ProjectionUpdate
			for k, d := range picked {
				when := base.Add(time.Duration(clock.Add(1)) * time.Second)
				u := reading("acme", tokenOf(d), when, []string{"temp", "hum"}[r.Intn(2)], float64(r.Intn(1000)))
				u.Identity = DeviceIdentity{ExternalId: "ext-" + tokenOf(d)}
				if k == 0 && n%8 == 7 {
					u.Presence = &PresenceTransition{Claim: presence.ClaimConnected, SessionId: session.Add(1), OccurredAt: when}
					u.Measurements = nil
				}
				batch = append(batch, u)
			}
			batches = append(batches, batch)
		}
		plan = append(plan, batches)
	}

	var wg sync.WaitGroup
	var failures atomic.Int64
	for w := range plan {
		api := a
		if w >= writers {
			api = b
		}
		wg.Add(1)
		go func(w int, api *Api) {
			defer wg.Done()
			for _, batch := range plan[w] {
				if err := api.MergeProjectionBatch(context.Background(), batch); err != nil {
					failures.Add(1)
					t.Errorf("writer %d: %v", w, err)
					return
				}
			}
		}(w, api)
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.FailNow()
	}
	for w := range plan {
		for _, batch := range plan[w] {
			if err := serial.MergeProjectionBatch(context.Background(), batch); err != nil {
				t.Fatalf("serial: %v", err)
			}
		}
	}

	got, want := orderFreeRows(t, a), orderFreeRows(t, serial)
	if len(want) == 0 {
		t.Fatal("the serial run left nothing to compare")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("two replicas left a different projection than one writer.\nconcurrent:\n  %s\nserial:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// 🔴 THE LOCK-ORDER INVERSION, MADE DETERMINISTIC. Two batch transactions deadlock only when
// each holds a device row the other is waiting for, and that needs a gap: one of them must be
// held mid-statement at exactly the right point. These tests make the gap with a third
// transaction (holdInsert) that has inserted a device's row and not committed — every other
// inserter of that device waits for it — and release it only once the other writer has either
// finished or is visibly waiting for the held batch (pg_blocking_pids). A path that takes rows
// out of token order then deadlocks every run (PostgreSQL aborts one side with 40P01 after
// deadlock_timeout), not one run in many.

// heldInsert is an open transaction, on a connection of its own, that has inserted one device's
// row and not committed.
type heldInsert struct {
	tx  *sql.Tx
	pid int
}

// holdInsert inserts tok's row for tenant acme in a transaction it leaves open: until it is
// rolled back, any other transaction inserting tok waits for it.
func holdInsert(t *testing.T, api *Api, tok string) *heldInsert {
	t.Helper()
	db, err := api.RDB.Database.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the held insert: %v", err)
	}
	h := &heldInsert{tx: tx}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&h.pid); err != nil {
		t.Fatalf("held insert's backend: %v", err)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO "device-state".device_states (created_at, updated_at, tenant_id, `+
		`device_token, external_id, source, active, inactivity_timeout, presence_source, session_id) `+
		`VALUES ($1, $1, 'acme', $2, '', '', false, 600, 'INFERRED', 0)`, now, tok); err != nil {
		t.Fatalf("the held insert of %s: %v", tok, err)
	}
	return h
}

// release rolls the held insert back: tok has no row again, and its waiters go on.
func (h *heldInsert) release(t *testing.T) {
	t.Helper()
	if err := h.tx.Rollback(); err != nil {
		t.Fatalf("release the held insert: %v", err)
	}
}

// blockedBy counts the backends waiting for a lock that pid holds.
func blockedBy(t *testing.T, api *Api, pid int) int {
	t.Helper()
	return countBackends(t, api, "$1 = ANY(pg_blocking_pids(pid))", pid)
}

// blockedByAWriter counts the backends of api's database waiting for a lock held by anything
// other than the held inserts: one batch writer waiting for another.
func blockedByAWriter(t *testing.T, api *Api, held ...*heldInsert) int {
	t.Helper()
	pids := make([]string, len(held))
	for i, h := range held {
		pids[i] = strconv.Itoa(h.pid)
	}
	return countBackends(t, api, "datname = current_database() AND cardinality(pg_blocking_pids(pid)) > 0 "+
		"AND NOT (pg_blocking_pids(pid) && ARRAY["+strings.Join(pids, ",")+"]::int[])")
}

func countBackends(t *testing.T, api *Api, where string, args ...any) int {
	t.Helper()
	db, err := api.RDB.Database.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE "+where,
		args...).Scan(&n); err != nil {
		t.Fatalf("count waiting backends: %v", err)
	}
	return n
}

// eventually polls cond until it holds or d passes, and reports which.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// mergeAsync runs one batch on api in the background; its error arrives on the channel.
func mergeAsync(api *Api, batch []ProjectionUpdate) chan error {
	done := make(chan error, 1)
	go func() { done <- api.MergeProjectionBatch(context.Background(), batch) }()
	return done
}

// finishedOrWaiting waits until the batch behind done has finished (its error, and true) or a
// batch writer is waiting for another (false). It fails the test if neither happens.
func finishedOrWaiting(t *testing.T, api *Api, done chan error, what string, held ...*heldInsert) (bool, error) {
	t.Helper()
	var err error
	finished := false
	if !eventually(15*time.Second, func() bool {
		select {
		case err = <-done:
			finished = true
			return true
		default:
			return blockedByAWriter(t, api, held...) > 0
		}
	}) {
		t.Fatalf("%s neither finished nor waited for another writer", what)
	}
	return finished, err
}

// await is the error of the batch behind done, failing the test if it never finishes.
func await(t *testing.T, done chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never finished", what)
		return nil
	}
}

// assertSerialState replays batches one after another into a database of its own and requires
// api's projection to equal it (on orderFreeRows).
func assertSerialState(t *testing.T, api *Api, batches ...[]ProjectionUpdate) {
	t.Helper()
	serial := newPostgresProjectionApi(t, "serialinv")
	for _, b := range batches {
		if err := serial.MergeProjectionBatch(context.Background(), b); err != nil {
			t.Fatalf("serial: %v", err)
		}
	}
	if got, want := orderFreeRows(t, api), orderFreeRows(t, serial); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the two batches left a different projection than merging them one after the other.\n"+
			"concurrent:\n  %s\nserial:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A CLAIM BATCH AND A HEARTBEAT BATCH, where a NEW device sorts below a KNOWN one: the shape that
// failed TestTwoReplicasMergingTheSameDevicesEndInTheSerialState intermittently with 40P01.
//
// dl-2 exists; dl-0 and dl-1 do not. The claim batch (dl-0, dl-1, claim on dl-2) is held at
// dl-0 by holdInsert. The heartbeat batch (dl-1, dl-2), from a second pool, then runs. A locked
// path that locks EXISTING rows first already holds dl-2 at that point, so the heartbeat batch
// inserts dl-1 and waits for dl-2; released, the claim batch inserts dl-0 and waits for dl-1 —
// a cycle. Taking every row in one token-ordered statement, the claim batch holds nothing while
// it waits at dl-0, and the heartbeat batch simply finishes.
func TestAClaimBatchAndAHeartbeatBatchOverNewAndKnownDevicesDoNotDeadlockOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "invhb")
	replica := newPostgresReplica(t, api)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	seed := []ProjectionUpdate{data("acme", "dl-2", t0)}
	if err := api.MergeProjectionBatch(context.Background(), seed); err != nil {
		t.Fatalf("seed dl-2: %v", err)
	}
	claimBatch := []ProjectionUpdate{reading("acme", "dl-0", t1, "temp", 1), reading("acme", "dl-1", t1, "temp", 1),
		claim("acme", "dl-2", t1, presence.ClaimConnected, 1)}
	heartbeatBatch := []ProjectionUpdate{reading("acme", "dl-1", t2, "temp", 2), reading("acme", "dl-2", t2, "temp", 2)}

	hold := holdInsert(t, api, "dl-0")
	claimDone := mergeAsync(api, claimBatch)
	if !eventually(15*time.Second, func() bool { return blockedBy(t, api, hold.pid) > 0 }) {
		t.Fatal("the claim batch never reached dl-0, so the test made no gap")
	}
	heartbeatDone := mergeAsync(replica, heartbeatBatch)
	heartbeatFinished, heartbeatErr := finishedOrWaiting(t, api, heartbeatDone, "the heartbeat batch", hold)
	hold.release(t)
	claimErr := await(t, claimDone, "the claim batch")
	if !heartbeatFinished {
		heartbeatErr = await(t, heartbeatDone, "the heartbeat batch")
	}
	if claimErr != nil || heartbeatErr != nil {
		t.Fatalf("claim batch: %v; heartbeat batch: %v — two batches took device rows in opposite orders "+
			"(the heartbeat batch waited for the claim batch: %v)", claimErr, heartbeatErr, !heartbeatFinished)
	}
	assertSerialState(t, api, seed, heartbeatBatch, claimBatch)
}

// TWO CLAIM BATCHES and a third writer that creates a row between them: the shape main's locked
// path already had (it locked the rows that existed, then inserted the rest, and a row created
// after its read was then locked last — out of order).
//
// Nothing exists. Batch A (dm-0, dm-2, claim on dm-3) is held at dm-0; then dm-3 is created and
// committed; then batch B (dm-1, dm-2, claim on dm-3), from a second pool, is held at dm-1. A
// path that reads existing rows first has B holding dm-3 (it existed at B's read but not at A's).
// A is released: it inserts dm-0 and dm-2, finds dm-3 created meanwhile and waits for B's lock on
// it. B is released: it inserts dm-1 and waits for A's dm-2 — a cycle. In one token-ordered
// statement neither batch holds anything while it waits, and A finishes before B is released.
func TestTwoClaimBatchesAroundARowCreatedBetweenThemDoNotDeadlockOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "invclaim")
	replica := newPostgresReplica(t, api)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	batchA := []ProjectionUpdate{reading("acme", "dm-0", t1, "temp", 1), reading("acme", "dm-2", t1, "temp", 1),
		claim("acme", "dm-3", t1, presence.ClaimConnected, 1)}
	created := []ProjectionUpdate{data("acme", "dm-3", t0)}
	batchB := []ProjectionUpdate{reading("acme", "dm-1", t2, "temp", 2), reading("acme", "dm-2", t2, "temp", 2),
		claim("acme", "dm-3", t2, presence.ClaimConnected, 2)}

	holdA, holdB := holdInsert(t, api, "dm-0"), holdInsert(t, api, "dm-1")
	aDone := mergeAsync(api, batchA)
	if !eventually(15*time.Second, func() bool { return blockedBy(t, api, holdA.pid) > 0 }) {
		t.Fatal("batch A never reached dm-0, so the test made no gap")
	}
	if err := api.MergeProjectionBatch(context.Background(), created); err != nil {
		t.Fatalf("the third writer's dm-3: %v", err)
	}
	bDone := mergeAsync(replica, batchB)
	if !eventually(15*time.Second, func() bool { return blockedBy(t, api, holdB.pid) > 0 }) {
		t.Fatal("batch B never reached dm-1, so the test made no gap")
	}
	holdA.release(t)
	aFinished, aErr := finishedOrWaiting(t, api, aDone, "batch A", holdB)
	holdB.release(t)
	bErr := await(t, bDone, "batch B")
	if !aFinished {
		aErr = await(t, aDone, "batch A")
	}
	if aErr != nil || bErr != nil {
		t.Fatalf("batch A: %v; batch B: %v — two batches took device rows in opposite orders "+
			"(batch A waited for batch B: %v)", aErr, bErr, !aFinished)
	}
	assertSerialState(t, api, created, batchA, batchB)
}

// orderFreeRows is the projection's rows minus what legitimately depends on arrival order
// (last_connect_time, the sweep-owned columns, row ids and stamps), one line per row.
func orderFreeRows(t *testing.T, api *Api) []string {
	t.Helper()
	var out []string
	for _, row := range projectionRows(t, api) {
		if strings.HasPrefix(row, "state ") {
			if i := strings.Index(row, " conn="); i >= 0 {
				if j := strings.Index(row[i:], " act="); j >= 0 {
					row = row[:i] + row[i+j:]
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// The upsert's guard names the target table without the area's schema prefix, which the
// insert carries: on PostgreSQL that has to resolve to the target, or every upsert fails.
// A batch across two tenants writes, updates and refuses by value.
func TestTheUpsertsRunOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "upsert")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	batch := []ProjectionUpdate{
		reading("acme", "d1", t0, "temp", 1), fix("acme", "d1", t0, 28),
		reading("globex", "d1", t0, "temp", 5),
	}
	if err := api.MergeProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		reading("acme", "d1", t0.Add(time.Second), "temp", 2), fix("acme", "d1", t0.Add(-time.Second), 29),
		reading("globex", "d1", t0, "temp", 6),
	}); err != nil {
		t.Fatalf("second batch: %v", err)
	}
	acme, globex := core.WithTenant(context.Background(), "acme"), core.WithTenant(context.Background(), "globex")
	if v := mustReading(t, api, acme, "d1", "temp").Value.Float64; v != 2 {
		t.Errorf("acme temp = %v; want the newer 2", v)
	}
	if lat := loadLocation(t, api, acme, "d1").Latitude.Float64; lat != 28 {
		t.Errorf("acme latitude = %v; the older fix must not replace 28", lat)
	}
	if v := mustReading(t, api, globex, "d1", "temp").Value.Float64; v != 5 {
		t.Errorf("globex temp = %v; an equal time must not replace 5", v)
	}
}

// The write-back of a batch's existing device rows is split so that no statement binds more
// than PostgreSQL's parameter limit: one row more than a statement can carry is two
// statements, and every row lands. MergeProjectionBatch puts no bound of its own on a batch.
func TestABatchOfMoreExistingDevicesThanOneStatementHoldsOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "wide")
	ctx := context.Background()
	acme := core.WithTenant(ctx, "acme")
	cols, err := foldColumns(api.RDB.DB(acme))
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	cols = append(cols, "updated_at")
	perStatement, err := rdb.RowsPerInsert(foldedStatesUpsert(api.RDB.DB(acme), cols), []DeviceState{})
	if err != nil {
		t.Fatalf("rows per statement: %v", err)
	}
	n := perStatement + 1
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	batchAt := func(when time.Time) []ProjectionUpdate {
		updates := make([]ProjectionUpdate, n)
		for i := range updates {
			updates[i] = data("acme", fmt.Sprintf("w-%05d", i), when)
		}
		return updates
	}
	if err := api.MergeProjectionBatch(ctx, batchAt(t0)); err != nil {
		t.Fatalf("first sight of %d devices: %v", n, err)
	}

	// Precondition: the same rows in ONE statement are refused by the driver, so only the
	// split lets the batch below through.
	t.Run("one statement is too large", func(t *testing.T) {
		var rows []DeviceState
		if err := api.RDB.DB(acme).Order("device_token").Find(&rows).Error; err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(rows) != n {
			t.Fatalf("%d rows seeded; want %d", len(rows), n)
		}
		probe := errors.New("roll back the probe")
		err := api.RDB.DB(acme).Transaction(func(tx *gorm.DB) error {
			if err := foldedStatesUpsert(tx, cols).Create(&rows).Error; err != nil {
				return err
			}
			return probe
		})
		if !rdb.IsStatementTooLarge(err) {
			t.Fatalf("%d rows in one statement: err = %v; want the driver's statement-too-large refusal", n, err)
		}
	})

	counter := rdbtest.NewStatementCounter("device_states")
	counted := NewApi(&rdb.RdbManager{Database: api.RDB.Database.Session(&gorm.Session{Logger: counter})})
	counter.Reset()
	if err := counted.MergeProjectionBatch(ctx, batchAt(t1)); err != nil {
		t.Fatalf("a batch of %d existing devices: %v", n, err)
	}
	if _, marked := counter.Counts(); marked != 2 {
		t.Errorf("the batch sent %d statements on device_states; want 2 (two heartbeat upserts)", marked)
	}
	countAt := func(when time.Time) int64 {
		var moved int64
		if err := api.RDB.DB(acme).Model(&DeviceState{}).Where("last_activity_time = ?", when).Count(&moved).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		return moved
	}
	if moved := countAt(t1); moved != int64(n) {
		t.Errorf("%d of %d devices at %v after the batch", moved, n, t1)
	}

	// The same devices in a batch with a claim take the locked path: lockOrCreate (two
	// statements, in token order), a lock read and the two write-backs.
	t2 := t1.Add(time.Minute)
	withClaim := batchAt(t2)
	withClaim[0] = claim("acme", "w-00000", t2, presence.ClaimConnected, 1)
	counter.Reset()
	if err := counted.MergeProjectionBatch(ctx, withClaim); err != nil {
		t.Fatalf("a batch of %d existing devices with a claim: %v", n, err)
	}
	if _, marked := counter.Counts(); marked != 5 {
		t.Errorf("the batch with a claim sent %d statements on device_states; want 5 (two lockOrCreate statements, "+
			"a lock read and two write-backs)", marked)
	}
	if moved := countAt(t2); moved != int64(n) {
		t.Errorf("%d of %d devices at %v after the batch with a claim", moved, n, t2)
	}
}
