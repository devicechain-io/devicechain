// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newTestStore spins up an in-memory sqlite database with the tenant-scope
// callbacks registered and the DetectSnapshot table migrated, exactly as the
// production wiring does (minus the schema prefix, which sqlite has no notion of).
func newTestStore(t *testing.T) *SnapshotStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DetectSnapshot{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewSnapshotStore(&rdb.RdbManager{Database: db})
}

// A missing partition loads as ok=false with no error, so a fresh Instance starts
// from an empty engine rather than failing.
func TestSnapshotStoreLoadMissing(t *testing.T) {
	store := newTestStore(t)
	got, ok, err := store.Load(context.Background(), "singleton")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("expected no snapshot, got ok=%v snap=%+v", ok, got)
	}
}

// Save then Load round-trips every field, and a second Save for the same partition
// overwrites in place (one live checkpoint per partition) rather than inserting a
// duplicate.
func TestSnapshotStoreSaveLoadUpsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"watermark":"2026-07-09T12:00:00Z","lastSeq":42}`)

	if err := store.Save(ctx, &DetectSnapshot{
		PartitionId: "singleton",
		StreamSeq:   42,
		Watermark:   wm,
		Payload:     payload,
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	got, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if got.StreamSeq != 42 || !got.Watermark.Equal(wm) || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// A later checkpoint advances the same row.
	payload2 := []byte(`{"watermark":"2026-07-09T12:05:00Z","lastSeq":99}`)
	if err := store.Save(ctx, &DetectSnapshot{
		PartitionId: "singleton",
		StreamSeq:   99,
		Watermark:   wm.Add(5 * time.Minute),
		Payload:     payload2,
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, ok, err = store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load after upsert: ok=%v err=%v", ok, err)
	}
	if got.StreamSeq != 99 || !bytes.Equal(got.Payload, payload2) {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	// Exactly one row survives the upsert.
	var count int64
	if err := store.rdb.Database.Model(&DetectSnapshot{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 snapshot row, got %d", count)
	}
}

// Save is monotonic: a lower stream sequence than what is already durable is refused
// with ErrStaleCheckpoint (so the caller does not ack-and-drop), and the existing row
// is left intact. An equal sequence is allowed (an idempotent re-commit).
func TestSnapshotStoreRefusesBackwardSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm, Payload: []byte("hi")}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 50, Watermark: wm, Payload: []byte("lo")})
	if !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("expected ErrStaleCheckpoint on backward write, got %v", err)
	}

	got, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.StreamSeq != 100 || !bytes.Equal(got.Payload, []byte("hi")) {
		t.Fatalf("stale write mutated the row: %+v", got)
	}

	// An equal sequence is a permitted idempotent re-commit.
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm, Payload: []byte("re")}); err != nil {
		t.Fatalf("equal-seq re-commit rejected: %v", err)
	}
}

// Reset is the ONE write in this store that moves a checkpoint backward on purpose, and it
// is issued from inside a leadership term BUILD — after the lease is acquired, before the
// term is live, across a restore, three view builds and a whole replay. That is a long
// enough window to lose the lease in, and a warm standby means there is a successor
// waiting to take it.
//
// 🔑 THE ORACLE IS THE ROW, NOT THE ERROR. A refusal that still deleted would satisfy an
// error-only assertion while destroying the successor's checkpoint — which is not merely
// state: it is the monotonic FLOOR every other guard in this file compares against.
func TestResetRefusesToClearACheckpointThatHasMovedOn(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	// The row this replica restored from.
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 40, Watermark: wm, Payload: []byte("mine")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The successor takes the partition and commits while we are still building our term.
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 77, Watermark: wm, Payload: []byte("theirs")}); err != nil {
		t.Fatalf("successor save: %v", err)
	}

	err := store.Reset(ctx, "singleton", 40)
	if !errors.Is(err, ErrResetRaced) {
		t.Fatalf("Reset against a row that has moved returned %v, want ErrResetRaced", err)
	}

	got, ok, err := store.Load(ctx, "singleton")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok {
		t.Fatal("the successor's checkpoint was DELETED by a replica that had lost the " +
			"partition; the row is the monotonic floor every other split-brain guard compares against")
	}
	if got.StreamSeq != 77 || !bytes.Equal(got.Payload, []byte("theirs")) {
		t.Fatalf("the successor's row was mutated: %+v", got)
	}
}

// The counterweight, and without it the guard above is satisfied by a Reset that refuses
// everything — which would turn the re-created/truncated-stream recovery into a permanent
// term-build failure and a crash-looping pod.
//
// The row the caller actually read is still deleted, which is the whole reason Reset
// exists: a snapshot ahead of the stream head cannot be walked forward, and Save's
// monotonic guard is precisely what stops it being overwritten.
func TestResetClearsTheCheckpointItRead(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 40, Watermark: wm, Payload: []byte("mine")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Reset(ctx, "singleton", 40); err != nil {
		t.Fatalf("Reset against the row it read: %v", err)
	}
	if _, ok, err := store.Load(ctx, "singleton"); err != nil || ok {
		t.Fatalf("the stale row survived its own reset: ok=%v err=%v", ok, err)
	}
}

// A partition with no row at all resets cleanly rather than erroring: two replicas can
// reach the same conclusion about the same truncated stream, and the second must not turn
// a completed recovery into a term-build failure.
func TestResetOfAnAbsentCheckpointIsANoOp(t *testing.T) {
	store := newTestStore(t)
	if err := store.Reset(context.Background(), "singleton", 0); err != nil {
		t.Fatalf("Reset with no row present: %v", err)
	}
}
