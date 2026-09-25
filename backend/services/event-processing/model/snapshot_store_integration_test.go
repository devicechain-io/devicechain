// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The DETECT checkpoint store's lock read on a real PostgreSQL.
//
// The unit tests run on in-memory sqlite, whose dialector drops FOR UPDATE from the SQL it
// builds: they can pin the projection and that the Locking clause is set, but not that the
// server actually serializes a second writer behind a narrowed, column-list FOR UPDATE, nor
// that the projected watermark scans back with the precision the equal-sequence guard
// compares. Those are Postgres facts, so they are tested on Postgres.
//
// Tagged `integration` so it stays out of the default `go test ./...`. Run it against a
// throwaway server (hack/integration-tests.sh does exactly this):
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run Postgres -v
package model

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
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

// newPostgresSnapshotStore runs the REAL event-processing migration chain through the REAL
// core/rdb path against a live PostgreSQL, empties detect_snapshots, and returns a store on
// it. The password default is "postgres", the one every other integration suite and
// hack/integration-tests.sh use.
func newPostgresSnapshotStore(tb testing.TB) *SnapshotStore {
	tb.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		tb.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"), itEnv("DC_IT_PGPASSWORD", "postgres")
	const instance = "dcsnapshot"
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		tb.Fatalf("creating the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres95",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		tb.Fatalf("run the event-processing migrations on the real server: %v", err)
	}
	tb.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	// The instance id is fixed, so a server that ran this before still holds its row.
	if err := mgr.Database.Exec(`TRUNCATE TABLE "event-processing".detect_snapshots`).Error; err != nil {
		tb.Fatalf("truncate detect_snapshots before the run: %v", err)
	}
	return NewSnapshotStore(mgr)
}

// waitForBlockedBackend polls until at least one backend in this database is waiting on a lock.
func waitForBlockedBackend(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n).Error; err != nil {
			t.Fatalf("probe pg_stat_activity: %v", err)
		}
		if n >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no backend ever blocked on a lock: the narrowed checkpoint read did not take a row " +
		"lock on Postgres, so two split-brain writers would compare against the same floor")
}

// The narrowed lock read on the real server: it is emitted as a column-list FOR UPDATE, it
// still SERIALIZES a second writer behind it, and the watermark it projects compares exactly
// as Load's does, so both monotonic guards and Reset's compare-and-swap keep their answers.
func TestSnapshotLockReadOnPostgres(t *testing.T) {
	store := newPostgresSnapshotStore(t)
	db := store.rdb.Database
	ctx := context.Background()
	// Microsecond precision: timestamptz stores microseconds, so this is the finest value that
	// round-trips unchanged.
	wm := time.Date(2026, 9, 25, 12, 0, 0, 123456000, time.UTC)

	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm, Payload: []byte("seed")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 1. What the server is sent.
	reads := recordLockReads(t, db)
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm, Payload: []byte("seed")}); err != nil {
		t.Fatalf("equal-seq, equal-watermark re-commit refused on Postgres: %v", err)
	}
	got := reads()
	if len(got) != 1 {
		t.Fatalf("Save took %d FOR UPDATE reads, want 1", len(got))
	}
	if want := []string{"stream_seq", "watermark"}; !reflect.DeepEqual(got[0].projection, want) || !strings.Contains(got[0].sql, "FOR UPDATE") {
		t.Fatalf("lock read on Postgres was %q: want a FOR UPDATE projecting %v", got[0].sql, want)
	}

	// 2. The guards, on the server's own scan of the projected columns.
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 99, Watermark: wm, Payload: []byte("x")}); !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("backward seq on Postgres: got %v, want ErrStaleCheckpoint", err)
	}
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm.Add(-time.Microsecond), Payload: []byte("x")}); !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("equal seq, watermark 1us lower on Postgres: got %v, want ErrStaleCheckpoint", err)
	}
	if snap, ok, err := store.Load(ctx, "singleton"); err != nil || !ok || snap.StreamSeq != 100 || !snap.Watermark.Equal(wm) || !bytes.Equal(snap.Payload, []byte("seed")) {
		t.Fatalf("refused writes moved the row: ok=%v err=%v snap=%+v", ok, err, snap)
	}

	// 3. The narrowed read still holds the row: a second writer waits behind it.
	txA := db.WithContext(ctx).Begin()
	floor, err := lockSnapshotFloor(txA, "singleton")
	if err != nil {
		txA.Rollback()
		t.Fatalf("lock the floor: %v", err)
	}
	if floor.StreamSeq != 100 || !floor.Watermark.Equal(wm) {
		txA.Rollback()
		t.Fatalf("projected floor scanned as seq=%d wm=%v, want 100 / %v", floor.StreamSeq, floor.Watermark, wm)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 101, Watermark: wm, Payload: []byte("next")})
	}()
	waitForBlockedBackend(t, db)
	select {
	case err := <-done:
		txA.Rollback()
		t.Fatalf("a second Save completed (%v) while the floor was locked", err)
	default:
	}
	if err := txA.Commit().Error; err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting Save failed once the lock was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the waiting Save never completed after the lock was released")
	}

	// 4. Reset's compare-and-swap reads the same floor.
	if err := store.Reset(ctx, "singleton", 100); !errors.Is(err, ErrResetRaced) {
		t.Fatalf("Reset against a moved row on Postgres: got %v, want ErrResetRaced", err)
	}
	if err := store.Reset(ctx, "singleton", 101); err != nil {
		t.Fatalf("Reset against the row it read on Postgres: %v", err)
	}
	if _, ok, err := store.Load(ctx, "singleton"); err != nil || ok {
		t.Fatalf("the row survived its own reset: ok=%v err=%v", ok, err)
	}
}

// BenchmarkSnapshotSavePostgres times Save against a real server at three previous-payload
// sizes. The payload is random bytes so TOAST cannot compress it away; real engine state is
// JSON and compresses, so these are an upper end for a given size. Over loopback, so the
// transfer half of the cost is a floor of what a pod network pays.
func BenchmarkSnapshotSavePostgres(b *testing.B) {
	store := newPostgresSnapshotStore(b)
	ctx := context.Background()
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(1))
	for _, sz := range []struct {
		name string
		n    int
	}{{"64KiB", 64 << 10}, {"1MiB", 1 << 20}, {"8MiB", 8 << 20}} {
		b.Run(sz.name, func(b *testing.B) {
			payload := make([]byte, sz.n)
			rng.Read(payload)
			if err := store.Reset(ctx, "singleton", mustCommittedSeq(b, store)); err != nil {
				b.Fatalf("clear: %v", err)
			}
			if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 1, Watermark: wm, Payload: payload}); err != nil {
				b.Fatalf("seed: %v", err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: int64(i + 2), Watermark: wm, Payload: payload}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSnapshotLockReadPostgres times the lock read ALONE — BEGIN, lockSnapshotFloor,
// ROLLBACK — against a row holding a random (incompressible) payload of each size. Save's
// timing above is dominated by writing the new payload, which this change does not touch;
// this isolates the half it does. Loopback, so a floor of what a pod network pays.
func BenchmarkSnapshotLockReadPostgres(b *testing.B) {
	store := newPostgresSnapshotStore(b)
	ctx := context.Background()
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(1))
	for _, sz := range []struct {
		name string
		n    int
	}{{"64KiB", 64 << 10}, {"1MiB", 1 << 20}, {"8MiB", 8 << 20}} {
		b.Run(sz.name, func(b *testing.B) {
			payload := make([]byte, sz.n)
			rng.Read(payload)
			if err := store.Reset(ctx, "singleton", mustCommittedSeq(b, store)); err != nil {
				b.Fatalf("clear: %v", err)
			}
			if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 1, Watermark: wm, Payload: payload}); err != nil {
				b.Fatalf("seed: %v", err)
			}
			db := store.rdb.Database.WithContext(ctx)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx := db.Begin()
				f, err := lockSnapshotFloor(tx, "singleton")
				tx.Rollback()
				if err != nil || f.StreamSeq != 1 {
					b.Fatalf("lock read: seq=%d err=%v", f.StreamSeq, err)
				}
			}
		})
	}
}

func mustCommittedSeq(b *testing.B, store *SnapshotStore) int64 {
	seq, _, err := store.LoadCommittedSeq(context.Background(), "singleton")
	if err != nil {
		b.Fatalf("read committed seq: %v", err)
	}
	return seq
}
