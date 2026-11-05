// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// orderWriter records each derived edge together with whether the DETECT snapshot was already
// committed when it was written, which is the deliver-before-checkpoint order under test.
type orderWriter struct {
	mu sync.Mutex
	edgeWriter
	dedups []string
	store  interface {
		LoadCommittedSeq(context.Context, string) (int64, bool, error)
	}
	partition    string
	savedAtWrite []bool
}

func (w *orderWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range msgs {
		w.dedups = append(w.dedups, m.DedupID)
	}
	_, saved, _ := w.store.LoadCommittedSeq(ctx, w.partition)
	for range msgs {
		w.savedAtWrite = append(w.savedAtWrite, saved)
	}
	return w.edgeWriter.WriteMessages(ctx, msgs...)
}
func (w *orderWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

// disarmedByRecheckProc is raisedAbsenceProc after the device's roster row is gone and the loop has
// run the REAL applyArmRecheck: the engine holds an undrained Resolved, no message has arrived since,
// and nothing else is pending.
func disarmedByRecheckProc(t *testing.T) (*ResolvedEventsProcessor, *orderWriter) {
	t.Helper()
	rp, _ := raisedAbsenceProc(t)
	rp.Store = newSingleConnStore(t) // the loop test checkpoints from another goroutine
	w := &orderWriter{store: rp.Store, partition: rp.cfg.PartitionId}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	rp.RosterStore = newProcRosterStore(t) // no row: the device reads as departed
	rp.dirty = false
	rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
	if len(rp.pendingDets) != 0 || len(w.got) != 0 {
		t.Fatalf("setup: the recheck must leave the Resolved in the engine, got pending=%d published=%v", len(rp.pendingDets), w.got)
	}
	return rp, w
}

func assertResolvedBeforeSnapshot(t *testing.T, rp *ResolvedEventsProcessor, w *orderWriter) {
	t.Helper()
	if len(w.got) != 1 || w.got[0] != "dev1:resolved" {
		t.Fatalf("the disarm's Resolved was not published, got %v", w.got)
	}
	if w.savedAtWrite[0] {
		t.Fatal("the Resolved was published AFTER the snapshot committed")
	}
	if _, saved, _ := rp.Store.LoadCommittedSeq(context.Background(), rp.cfg.PartitionId); !saved {
		t.Fatal("the checkpoint committed no snapshot of the cleared latch")
	}
}

// TestCheckpointPublishesDisarmResolvedWithNoMessage: a disarm queues a Resolved in the engine; with
// no message after it, a checkpoint must drain and publish it before the snapshot that carries the
// cleared latch, or the alarm stays raised forever.
func TestCheckpointPublishesDisarmResolvedWithNoMessage(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.dirty = true // other state changed, so the checkpoint commits
	if !rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint deferred")
	}
	assertResolvedBeforeSnapshot(t, rp, w)
}

// TestFinalCheckpointPublishesDisarmResolved: the term-end flush, which used to return early when
// the loop was clean and nothing was buffered.
func TestFinalCheckpointPublishesDisarmResolved(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.finalCheckpoint()
	assertResolvedBeforeSnapshot(t, rp, w)
}

// TestCheckpointDrainDoesNotRepublish: a second checkpoint publishes nothing further.
func TestCheckpointDrainDoesNotRepublish(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.finalCheckpoint()
	rp.finalCheckpoint()
	rp.checkpoint(context.Background())
	if len(w.got) != 1 {
		t.Fatalf("want exactly one Resolved, got %v", w.got)
	}
}

// edgesNow is a race-safe copy of what has been written, for tests that poll a running loop.
func (w *orderWriter) edgesNow() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.got...)
}

// TestTickDeliversDisarmResolvedOnAQuietStream: after RemoveExpected cancels the timer nothing is
// pending, the idle advance never runs, and nothing else dirties the loop. The tick's own drain is
// the only thing that makes the interval checkpoint fire and deliver the clear. Driven through the
// real run() on a clock the test moves.
func TestTickDeliversDisarmResolvedOnAQuietStream(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	if rp.engine.HasPendingWork() {
		t.Fatal("setup: the disarm must leave no pending work, or the idle advance would run")
	}
	clock := newAtomicClock(testBase)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rp.procCtx, rp.procCancel = ctx, cancel
	rp.clock = clock
	rp.cfg.Clock = clock
	rp.cfg.CheckpointInterval = 10 * time.Second
	rp.cfg.TickInterval = 5 * time.Millisecond
	rp.lastCheckpoint = testBase
	rp.ResolvedEventsReader = &fakeReader{}
	rp.readerWG.Add(1)
	go rp.run()
	t.Cleanup(func() { rp.pcancel(); rp.readerWG.Wait() })

	// Inside the interval nothing is due: the clear waits for the checkpoint.
	time.Sleep(100 * time.Millisecond)
	if got := w.edgesNow(); len(got) != 0 {
		t.Fatalf("published before the checkpoint interval elapsed: %v", got)
	}
	clock.add(11 * time.Second)
	waitFor(t, "the interval checkpoint to deliver the Resolved and commit", func() bool {
		_, saved, _ := rp.Store.LoadCommittedSeq(context.Background(), rp.cfg.PartitionId)
		return len(w.edgesNow()) == 1 && saved
	})
	rp.pcancel()
	rp.readerWG.Wait()
	assertResolvedBeforeSnapshot(t, rp, w)
}

// TestDisarmThenMessageKeepsOrderIdentityAndNotEarly: a disarm, then a message for ANOTHER series
// that fires its own detection, then a checkpoint. The Resolved goes first, carries the DedupID a
// publisher derives for it from the detection alone, and is not marked early.
func TestDisarmThenMessageKeepsOrderIdentityAndNotEarly(t *testing.T) {
	absID := runtime.ComposeRuleID("acme", "p1@v1/r1")
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{absScopedProc("acme", "p1@v1", absID), hotScoped(t)})
	rp, _ := raisedAbsenceProc(t)
	eng := detectcore.NewEngine(reg.Cores(), 0)
	rp.engine, rp.registry = eng, reg
	rp.armer = runtime.NewDeadmanArmer(reg, eng)
	w := &orderWriter{store: rp.Store, partition: rp.cfg.PartitionId}
	rp.publisher = runtime.NewPublisher(w, reg, (*DetectMetrics)(nil))
	rp.RosterStore = newProcRosterStore(t)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)
	eng.Advance(testBase.Add(11 * time.Second))
	rp.drainDetections(testBase.Add(11*time.Second), true)
	if !rp.publishPending(context.Background()) || len(w.got) != 1 {
		t.Fatalf("setup: raise not published: %v", w.got)
	}
	w.got, w.dedups, w.savedAtWrite = nil, nil, nil

	eng.Skip(19) // the stream is contiguous up to the message below (no gap fill)
	wm := eng.Watermark()
	rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"}) // Resolved queued, stamped at wm
	rp.handle(measuredMsg(t, 20, "acme", "other", "p@1", "temperature", "90", &fakeAck{}))

	if len(rp.pendingDets) != 2 {
		t.Fatalf("want [Resolved, raise] buffered, got %d", len(rp.pendingDets))
	}
	if rp.pendingDets[0].Edge != detectcore.EdgeResolved || rp.pendingDets[0].early {
		t.Fatalf("the disarm's Resolved must be first and not early: %+v", rp.pendingDets[0])
	}
	if !rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint deferred")
	}
	if len(w.got) != 2 || w.got[0] != "dev1:resolved" || w.got[1] != "other:raised" {
		t.Fatalf("want [dev1:resolved other:raised], got %v", w.got)
	}
	// The expected id comes from the detection alone, with an unrelated trigger time: the id must not
	// depend on when or by which path the detection was drained.
	pre, ok := rp.publisher.Prepare(detectcore.Detection{RuleID: absID, Series: "dev1", Kind: detectcore.Absence, At: wm, Edge: detectcore.EdgeResolved}, testBase.Add(99*time.Hour))
	if !ok {
		t.Fatal("prepare refused the Resolved")
	}
	if w.dedups[0] != pre.DedupID {
		t.Fatalf("Resolved DedupID changed: got %q want %q", w.dedups[0], pre.DedupID)
	}
}

func hotScoped(t *testing.T) runtime.ScopedRule {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/hot", Name: "hot", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}
}

// newSingleConnStore is newTestStore pinned to one connection: every connection to ":memory:" is its
// own empty database, so a checkpoint from the loop goroutine could otherwise land on a fresh one.
func newSingleConnStore(t *testing.T) *model.SnapshotStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DetectSnapshot{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewSnapshotStore(&rdb.RdbManager{Database: db})
}
