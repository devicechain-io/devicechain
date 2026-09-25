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
func newTestStore(t testing.TB) *SnapshotStore {
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

// Save is monotonic on the watermark too: at an EQUAL sequence a lower watermark is a
// split-brain peer moving the logical clock backward, and it is refused with the row left
// exactly as it was. This is also the mapping guard for the watermark half of the locked
// read — a watermark that scanned as the zero time would make Before() answer false and
// let this write through.
func TestSnapshotStoreRefusesBackwardWatermarkAtEqualSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm, Payload: []byte("seed")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: wm.Add(-time.Minute), Payload: []byte("lagging")})
	if !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("equal seq, lower watermark: got %v, want ErrStaleCheckpoint", err)
	}
	got, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.StreamSeq != 100 || !got.Watermark.Equal(wm) || !bytes.Equal(got.Payload, []byte("seed")) {
		t.Fatalf("a refused backward-watermark write mutated the row: seq=%d wm=%v payload=%q",
			got.StreamSeq, got.Watermark, got.Payload)
	}

	// The counterweight: a HIGHER watermark at the same sequence is the ordinary idle
	// advance and must land, or the guard above is satisfied by one that refuses everything.
	ahead := wm.Add(time.Minute)
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 100, Watermark: ahead, Payload: []byte("ahead")}); err != nil {
		t.Fatalf("equal seq, higher watermark rejected: %v", err)
	}
	got, ok, err = store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !got.Watermark.Equal(ahead) || !bytes.Equal(got.Payload, []byte("ahead")) {
		t.Fatalf("the idle advance did not land: wm=%v payload=%q", got.Watermark, got.Payload)
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

// Every guard compares a partition against ITS OWN row. Production binds one partition
// today, so nothing else in this file ever holds two rows, and a lock read that matched
// whatever row came first would pass all of it.
//
// The two partitions are seeded so that reading the OTHER partition's floor flips an
// answer in BOTH directions, whichever row a mis-scoped read happens to return: B's
// forward write would be refused against A's higher floor, and A's backward write would be
// accepted against B's lower one. Reset is checked the same way, and the oracle is the
// rows, not just the errors.
func TestSnapshotGuardsCompareEachPartitionOnlyAgainstItsOwnRow(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "a", StreamSeq: 100, Watermark: wm, Payload: []byte("a100")}); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "b", StreamSeq: 10, Watermark: wm, Payload: []byte("b10")}); err != nil {
		t.Fatalf("seeding b (a new partition) was refused: it was compared against a's floor: %v", err)
	}

	// B moves forward against its own floor (10), not A's (100).
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "b", StreamSeq: 50, Watermark: wm, Payload: []byte("b50")}); err != nil {
		t.Fatalf("b's forward save (10 -> 50) was refused: it was compared against another partition's floor: %v", err)
	}
	// A moving backward is refused against its own floor (100), not B's (now 50).
	if err := store.Save(ctx, &DetectSnapshot{PartitionId: "a", StreamSeq: 60, Watermark: wm, Payload: []byte("a60")}); !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("a's backward save (100 -> 60) returned %v, want ErrStaleCheckpoint: it was compared against another partition's floor", err)
	}

	// Reset's compare-and-swap reads its own partition too: B's sequence is not A's.
	if err := store.Reset(ctx, "a", 50); !errors.Is(err, ErrResetRaced) {
		t.Fatalf("Reset(a, 50) returned %v, want ErrResetRaced: 50 is b's sequence, not a's", err)
	}
	if err := store.Reset(ctx, "b", 50); err != nil {
		t.Fatalf("Reset(b, 50) against the row it read: %v", err)
	}

	if _, ok, err := store.Load(ctx, "b"); err != nil || ok {
		t.Fatalf("b survived its own reset: ok=%v err=%v", ok, err)
	}
	got, ok, err := store.Load(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("a was removed by operations on b: ok=%v err=%v", ok, err)
	}
	if got.StreamSeq != 100 || !bytes.Equal(got.Payload, []byte("a100")) {
		t.Fatalf("a's row was moved by operations on the other partition: %+v", got)
	}
}
