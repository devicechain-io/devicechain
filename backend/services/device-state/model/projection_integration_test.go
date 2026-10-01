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
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
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
func newPostgresProjectionApi(t *testing.T, name string) *Api {
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
func truncateProjection(t *testing.T, api *Api) {
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

// A device's row created by another writer between the batch's lock read and its insert, on
// PostgreSQL: the INSERT … ON CONFLICT DO NOTHING reports no row, and the batch folds its
// events into the row that is there. The other writer is simulated inside the batch's own
// transaction, right after its lock read, as the SQLite test does.
func TestABatchFoldsIntoARowAnotherWriterCreatedMeanwhileOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "race")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	armed := true
	if err := api.RDB.Database.Callback().Query().After("gorm:query").Register("test:create_meanwhile", func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "device_states" {
			return
		}
		armed = false
		if _, err := tx.Statement.ConnPool.ExecContext(context.Background(),
			`INSERT INTO "device-state".device_states (created_at, updated_at, tenant_id, device_token, external_id, `+
				`source, active, last_connect_time, last_activity_time, inactivity_timeout, presence_source, session_id) `+
				`VALUES ($1, $2, 'acme', 'race-1', 'theirs', '', true, $3, $4, 600, 'INFERRED', 0)`,
			t0, t0, t0, t0); err != nil {
			t.Errorf("the other writer's insert: %v", err)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(time.Minute), Identity: DeviceIdentity{Source: "mqtt1"}},
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("MergeProjectionBatch over a row created meanwhile: %v", err)
	}
	if armed {
		t.Fatal("the callback never ran, so this test did not put a row in the gap")
	}
	var rows []DeviceState
	if err := api.RDB.DB(core.WithTenant(context.Background(), "acme")).Where("device_token = ?", "race-1").Find(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for race-1; want exactly 1", len(rows))
	}
	if got := rows[0]; got.ExternalId != "theirs" || got.Source != "mqtt1" ||
		!got.LastActivityTime.Time.Equal(t0.Add(2*time.Minute)) || !got.LastConnectTime.Time.Equal(t0) {
		t.Errorf("row = ext %q src %q activity %v connect %v; want theirs/mqtt1, %v, %v", got.ExternalId, got.Source,
			got.LastActivityTime.Time, got.LastConnectTime.Time, t0.Add(2*time.Minute), t0)
	}
}

// 🔴 THE BATCH HOLDS EVERY ROW IT READS UNTIL IT COMMITS. Step 1 reads each existing row
// with SELECT … FOR UPDATE, folds the batch into its copy and writes it back. Without the lock
// that is an unlocked read-modify-write: another writer (here the single-event merge) commits
// a newer activity between the batch's read and its write-back, and the batch then writes its stale
// copy over it — the device's activity goes backwards. The other writer starts right after
// the batch's read, from a connection of its own, and must still be waiting for the row when
// the batch moves on; once the batch commits it reads the batch's row and moves it forward.
func TestABatchHoldsTheRowsItReadUntilItCommitsOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "lock")
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
	if err := api.RDB.Database.Callback().Query().After("gorm:query").Register("test:write_meanwhile", func(tx *gorm.DB) {
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
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "lock-1", OccurredAt: t0.Add(time.Minute)},
	}); err != nil {
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
		t.Error("the other writer committed while the batch still had the row it read: the batch does not lock it")
	}
	var rows []DeviceState
	if err := api.RDB.DB(ctx).Where("device_token = ?", "lock-1").Find(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for lock-1; want exactly 1", len(rows))
	}
	if got := rows[0].LastActivityTime.Time; !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("last activity = %v; want the other writer's newer %v — the batch wrote a stale copy over it",
			got, t0.Add(5*time.Minute))
	}
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
	cols, err := foldedStateColumns(api.RDB.DB(acme))
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
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
	if _, marked := counter.Counts(); marked != 3 {
		t.Errorf("the batch sent %d statements on device_states; want 3 (a lock read and two write-backs)", marked)
	}
	var moved int64
	if err := api.RDB.DB(acme).Model(&DeviceState{}).Where("last_activity_time = ?", t1).Count(&moved).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if moved != int64(n) {
		t.Errorf("%d of %d devices at %v after the batch", moved, n, t1)
	}
}
