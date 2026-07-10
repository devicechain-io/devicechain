// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const testSubject = "dc.acme.resolved-events"

var testBase = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

// fakeReader replays a fixed sequence of (message, error) results, then blocks on
// ctx so the read pump parks instead of spinning past the end of the script.
type fakeReader struct {
	results []readResult
	idx     int
	handled int
	lastErr error
}

type readResult struct {
	msg messaging.Message
	err error
}

func (r *fakeReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if r.idx < len(r.results) {
		res := r.results[r.idx]
		r.idx++
		return res.msg, res.err
	}
	<-ctx.Done()
	return messaging.Message{}, io.EOF
}

func (r *fakeReader) HandleResponse(err error) {
	r.handled++
	r.lastErr = err
}

// fakeAck records how a message was dispositioned so a test can assert the drop or
// checkpoint actually acknowledged (a plain struct-literal Message's Ack is a no-op).
type fakeAck struct {
	acks int
	naks int
}

func (a *fakeAck) Ack() error { a.acks++; return nil }
func (a *fakeAck) Nak() error { a.naks++; return nil }

// fakeReplayOpener yields a preset, ordered set of messages as a bounded replay from
// startSeq up to head — the in-order prefix the engine missed. It records the start
// sequence it was asked for so a test can assert replay began at snapshotSeq+1.
type fakeReplayOpener struct {
	msgs      []messaging.Message
	head      uint64
	lastStart uint64
}

func (f *fakeReplayOpener) NewReplayReader(suffix string, startSeq uint64) (messaging.ReplayReader, uint64, error) {
	f.lastStart = startSeq
	var out []messaging.Message
	for _, m := range f.msgs {
		if m.StreamSeq >= startSeq && m.StreamSeq <= f.head {
			out = append(out, m)
		}
	}
	return &fakeReplayReader{msgs: out}, f.head, nil
}

type fakeReplayReader struct {
	msgs   []messaging.Message
	idx    int
	closed bool
}

func (r *fakeReplayReader) Read(ctx context.Context) (messaging.Message, error) {
	if r.idx >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.idx]
	r.idx++
	return m, nil
}

func (r *fakeReplayReader) Close() error { r.closed = true; return nil }

// newTestStore spins up an in-memory sqlite snapshot store with the DetectSnapshot
// table migrated, exactly as production wiring does (minus the schema prefix).
func newTestStore(t *testing.T) *model.SnapshotStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DetectSnapshot{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewSnapshotStore(&rdb.RdbManager{Database: db})
}

// newTestProcessor builds a processor via struct literal (no Microservice, so
// metrics stay nil and the nil-safe recorders no-op) with the ticker/interval
// pushed far out so only explicit count/method calls drive checkpoints. The rule
// set is empty (this file exercises the checkpoint spine, not the fan-out); the
// registry-driven fan-out and derived publisher are covered in runtime_test.go.
func newTestProcessor(store *model.SnapshotStore, rules []detectcore.Rule, checkpointEvents int) *ResolvedEventsProcessor {
	scoped := make([]runtime.ScopedRule, 0, len(rules))
	for i := range rules {
		scoped = append(scoped, runtime.ScopedRule{Compiled: &rules0.CompiledRule{ID: rules[i].ID, Core: rules[i]}})
	}
	reg := runtime.NewRuleRegistry(scoped)
	return &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   checkpointEvents,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(&captureWriter{}, reg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   context.Background(),
	}
}

// resolvedBytes marshals a minimal resolved event whose occurred-time is the given
// instant (the value the loop uses to advance the watermark).
func resolvedBytes(t *testing.T, occurred time.Time) []byte {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:            "http1",
		SourceDeviceToken: "dev-1",
		OccurredTime:      occurred,
		ProcessedTime:     occurred,
		EventType:         esmodel.Measurement,
		Payload:           &dmmodel.ResolvedMeasurementsPayload{},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return b
}

// msgAt builds a consumed resolved-event message carrying stream sequence seq and
// occurred-time testBase+seq seconds.
func msgAt(t *testing.T, seq uint64, ack *fakeAck) messaging.Message {
	t.Helper()
	m := messaging.NewConsumedMessage(testSubject, resolvedBytes(t, testBase.Add(time.Duration(seq)*time.Second)), 0, nil, ack)
	m.StreamSeq = seq
	return m
}

// A checkpoint commits the snapshot and only then acks the whole buffer; before the
// checkpoint nothing is acked (ack-on-checkpoint).
func TestCheckpointCommitsThenAcks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	acks := make([]*fakeAck, 3)
	for i := uint64(1); i <= 3; i++ {
		acks[i-1] = &fakeAck{}
		rp.handle(msgAt(t, i, acks[i-1]))
	}
	for i, a := range acks {
		if a.acks != 0 {
			t.Fatalf("message %d acked before checkpoint (acks=%d)", i+1, a.acks)
		}
	}
	if !rp.dirty {
		t.Fatal("expected engine dirty after applying events")
	}

	rp.checkpoint(ctx)

	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load after checkpoint: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 3 {
		t.Fatalf("expected checkpoint at seq 3, got %d", snap.StreamSeq)
	}
	for i, a := range acks {
		if a.acks != 1 || a.naks != 0 {
			t.Fatalf("message %d not acked exactly once after checkpoint: acks=%d naks=%d", i+1, a.acks, a.naks)
		}
	}
	if len(rp.pendingAcks) != 0 || rp.dirty {
		t.Fatalf("expected clean state after checkpoint: pending=%d dirty=%v", len(rp.pendingAcks), rp.dirty)
	}
}

// After a checkpoint and a simulated restart, redelivery of already-applied events
// is dropped by the engine's idempotent guard (no double-apply), only genuinely new
// events advance the engine, and every redelivered message is still acked so it
// stops redelivering. This is the durable half of the correctness spine, end-to-end
// through the real snapshot store.
func TestRestoreReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// First run: apply seq 1..3, checkpoint.
	p1 := newTestProcessor(store, nil, 100)
	if err := p1.restore(ctx); err != nil {
		t.Fatalf("p1 restore: %v", err)
	}
	for i := uint64(1); i <= 3; i++ {
		p1.handle(msgAt(t, i, &fakeAck{}))
	}
	p1.checkpoint(ctx)
	if p1.engine.LastSeq() != 3 {
		t.Fatalf("p1 lastSeq = %d, want 3", p1.engine.LastSeq())
	}

	// Restart: a fresh processor restores from the same store.
	p2 := newTestProcessor(store, nil, 100)
	if err := p2.restore(ctx); err != nil {
		t.Fatalf("p2 restore: %v", err)
	}
	if p2.engine.LastSeq() != 3 {
		t.Fatalf("restored lastSeq = %d, want 3", p2.engine.LastSeq())
	}
	if want := testBase.Add(3 * time.Second); !p2.engine.Watermark().Equal(want) {
		t.Fatalf("restored watermark = %v, want %v", p2.engine.Watermark(), want)
	}

	// Redeliver the already-applied prefix: dropped by the idempotent guard (no
	// advance, not dirty) but still acked.
	dupeAcks := make([]*fakeAck, 3)
	for i := uint64(1); i <= 3; i++ {
		dupeAcks[i-1] = &fakeAck{}
		p2.handle(msgAt(t, i, dupeAcks[i-1]))
	}
	if p2.dirty {
		t.Fatal("redelivered duplicates must not mark the engine dirty")
	}
	if p2.engine.LastSeq() != 3 {
		t.Fatalf("duplicates advanced lastSeq to %d, want 3", p2.engine.LastSeq())
	}

	// New events past the checkpoint advance the engine.
	for i := uint64(4); i <= 5; i++ {
		p2.handle(msgAt(t, i, &fakeAck{}))
	}
	if !p2.dirty || p2.engine.LastSeq() != 5 {
		t.Fatalf("new events did not advance: dirty=%v lastSeq=%d", p2.dirty, p2.engine.LastSeq())
	}

	p2.checkpoint(ctx)
	snap, _, _ := store.Load(ctx, "singleton")
	if snap.StreamSeq != 5 {
		t.Fatalf("checkpoint after replay = %d, want 5", snap.StreamSeq)
	}
	for i, a := range dupeAcks {
		if a.acks != 1 {
			t.Fatalf("redelivered duplicate %d not acked (acks=%d) — would redeliver forever", i+1, a.acks)
		}
	}
}

// A poison payload and an untenanted subject are dropped (never advance the engine)
// but still acked; with no state change the checkpoint writes no snapshot.
func TestPoisonAndUntenantedDroppedAndAcked(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	poison := &fakeAck{}
	pm := messaging.NewConsumedMessage(testSubject, []byte("not-a-proto"), 0, nil, poison)
	pm.StreamSeq = 1
	rp.handle(pm)

	untenanted := &fakeAck{}
	um := messaging.NewConsumedMessage("no-tenant-here", resolvedBytes(t, testBase), 0, nil, untenanted)
	um.StreamSeq = 2
	rp.handle(um)

	if rp.dirty {
		t.Fatal("poison/untenanted messages must not mark the engine dirty")
	}

	rp.checkpoint(ctx)

	if _, ok, _ := store.Load(ctx, "singleton"); ok {
		t.Fatal("no snapshot should be written when nothing advanced the engine")
	}
	if poison.acks != 1 || untenanted.acks != 1 {
		t.Fatalf("dropped messages not acked: poison=%d untenanted=%d", poison.acks, untenanted.acks)
	}
}

// The full loop (read pump + select loop) drains a scripted reader, flushes a final
// checkpoint on EOF, and acks every message it consumed.
func TestRunLoopCheckpointsAndAcksOnEOF(t *testing.T) {
	store := newTestStore(t)
	a1, a2 := &fakeAck{}, &fakeAck{}
	reader := &fakeReader{results: []readResult{
		{msg: msgAt(t, 1, a1)},
		{msg: msgAt(t, 2, a2)},
		{err: io.EOF},
	}}
	emptyReg := runtime.NewRuleRegistry(nil)
	rp := &ResolvedEventsProcessor{
		ResolvedEventsReader: reader,
		Replay:               &fakeReplayOpener{head: 0}, // fresh: nothing to replay
		Store:                store,
		cfg: Config{
			PartitionId:        "singleton",
			Suffix:             "resolved-events",
			CheckpointEvents:   100,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  emptyReg,
		publisher: runtime.NewPublisher(&captureWriter{}, emptyReg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
	}
	ctx := context.Background()
	if err := rp.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := rp.ExecuteStart(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	rp.readerWG.Wait() // loop returns on EOF after flushing the final checkpoint

	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load after loop: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 2 {
		t.Fatalf("final checkpoint = %d, want 2", snap.StreamSeq)
	}
	if a1.acks != 1 || a2.acks != 1 {
		t.Fatalf("loop did not ack both messages: a1=%d a2=%d", a1.acks, a2.acks)
	}
}

// The startup replay phase reads the stream IN ORDER from the snapshot sequence up
// to the head and advances the engine to it BEFORE any live consumption — the fix
// for out-of-order post-crash redelivery: even if the live stream would deliver seq
// 8 before the held-back 6 and 7, replay applies 6,7,8 in order so no event is lost
// to the idempotent guard.
func TestReplayToHeadAppliesInOrder(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// First run: apply seq 1..5, checkpoint (durable snapshot at seq 5).
	p1 := newTestProcessor(store, nil, 100)
	if err := p1.restore(ctx); err != nil {
		t.Fatalf("p1 restore: %v", err)
	}
	for i := uint64(1); i <= 5; i++ {
		p1.handle(msgAt(t, i, &fakeAck{}))
	}
	p1.checkpoint(ctx)

	// Restart: restore to seq 5, then replay 6..8 in order (head=8).
	opener := &fakeReplayOpener{head: 8, msgs: []messaging.Message{
		msgAt(t, 6, &fakeAck{}), msgAt(t, 7, &fakeAck{}), msgAt(t, 8, &fakeAck{}),
	}}
	p2 := newTestProcessor(store, nil, 100)
	p2.Replay = opener
	p2.cfg.Suffix = "resolved-events"
	if err := p2.restore(ctx); err != nil {
		t.Fatalf("p2 restore: %v", err)
	}
	if p2.engine.LastSeq() != 5 {
		t.Fatalf("restored lastSeq = %d, want 5", p2.engine.LastSeq())
	}
	if err := p2.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}

	if opener.lastStart != 6 {
		t.Fatalf("replay started at seq %d, want 6 (snapshotSeq+1)", opener.lastStart)
	}
	if p2.engine.LastSeq() != 8 {
		t.Fatalf("engine did not replay to head: lastSeq = %d, want 8", p2.engine.LastSeq())
	}
	if want := testBase.Add(8 * time.Second); !p2.engine.Watermark().Equal(want) {
		t.Fatalf("watermark after replay = %v, want %v", p2.engine.Watermark(), want)
	}
	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load after replay: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 8 {
		t.Fatalf("replay did not checkpoint the head: seq = %d, want 8", snap.StreamSeq)
	}
}

// When Save refuses a backward (stale/split-brain) checkpoint, the loop must NOT ack
// its buffered messages — they stay pending to redeliver rather than being
// acked-and-dropped with their effects in no durable snapshot.
func TestCheckpointDoesNotAckOnStaleRefusal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Another (leading) writer is already at seq 100.
	if err := store.Save(ctx, &model.DetectSnapshot{
		PartitionId: "singleton", StreamSeq: 100, Watermark: testBase, Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed leading snapshot: %v", err)
	}

	// A lagging engine at seq 2 tries to checkpoint (bypass restore so it does not
	// adopt the leading row's sequence).
	rp := newTestProcessor(store, nil, 100)
	rp.engine = detectcore.NewEngine(nil, 0)
	acks := []*fakeAck{{}, {}}
	rp.handle(msgAt(t, 1, acks[0]))
	rp.handle(msgAt(t, 2, acks[1]))

	rp.checkpoint(ctx) // Save(2) < existing 100 → ErrStaleCheckpoint

	for i, a := range acks {
		if a.acks != 0 {
			t.Fatalf("message %d was acked despite a stale-checkpoint refusal (acks=%d)", i+1, a.acks)
		}
	}
	if len(rp.pendingAcks) != 2 || !rp.dirty {
		t.Fatalf("expected buffer/dirty preserved after refusal: pending=%d dirty=%v", len(rp.pendingAcks), rp.dirty)
	}
	if got, _, _ := store.Load(ctx, "singleton"); got.StreamSeq != 100 {
		t.Fatalf("leading row was clobbered: seq=%d, want 100", got.StreamSeq)
	}
	// The stale refusal latches: this writer lost the split brain and halts, so a subsequent
	// checkpoint is a no-op (no further derived-event emission, no ack).
	if !rp.stale {
		t.Fatalf("a stale refusal must latch the writer as stale")
	}
	rp.checkpoint(ctx)
	for i, a := range acks {
		if a.acks != 0 {
			t.Fatalf("message %d acked after the writer went stale (acks=%d)", i+1, a.acks)
		}
	}
}

// When the stream head is behind the snapshot (a re-created/truncated stream vs. a
// stale snapshot), replay resets the engine to empty rather than replaying against
// sequences the stream no longer holds.
func TestReplayResetsWhenStreamBehindSnapshot(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	p1 := newTestProcessor(store, nil, 100)
	if err := p1.restore(ctx); err != nil {
		t.Fatalf("p1 restore: %v", err)
	}
	for i := uint64(1); i <= 5; i++ {
		p1.handle(msgAt(t, i, &fakeAck{}))
	}
	p1.checkpoint(ctx)

	// Restart against a stream whose head (2) is behind the snapshot (5).
	p2 := newTestProcessor(store, nil, 100)
	p2.Replay = &fakeReplayOpener{head: 2}
	p2.cfg.Suffix = "resolved-events"
	if err := p2.restore(ctx); err != nil {
		t.Fatalf("p2 restore: %v", err)
	}
	if err := p2.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if p2.engine.LastSeq() != 0 {
		t.Fatalf("engine not reset: lastSeq = %d, want 0", p2.engine.LastSeq())
	}
	// The stale row is cleared so the next checkpoint starts fresh.
	if _, ok, _ := store.Load(ctx, "singleton"); ok {
		t.Fatal("stale snapshot row should have been reset/deleted")
	}
}
