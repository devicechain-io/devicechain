// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// These tests pin how a checkpoint's deadlines are shaped: one per network call, adapting to a
// store that is slow but working, and backing off after a failure.

// hang as a delay means "never answer".
const hang = time.Duration(-1)

// newSlowStore is a snapshot store whose queries each take the returned delay (settable while
// the test runs) before they answer, or never answer at all while it is hang. It honours the
// query's context either way, so a deadline cuts a slow answer short.
func newSlowStore(t *testing.T, initial time.Duration) (*model.SnapshotStore, *atomic.Int32, *atomic.Int64) {
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
	var entered atomic.Int32
	var delay atomic.Int64
	delay.Store(int64(initial))
	err = db.Callback().Query().Before("gorm:query").Register("test:slow", func(tx *gorm.DB) {
		entered.Add(1)
		ctx := tx.Statement.Context
		d := time.Duration(delay.Load())
		if d == 0 {
			return
		}
		var after <-chan time.Time
		if d > 0 {
			after = time.After(d)
		}
		select {
		case <-after:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	})
	if err != nil {
		t.Fatalf("register callback: %v", err)
	}
	return model.NewSnapshotStore(&rdb.RdbManager{Database: db}), &entered, &delay
}

// slowWriter is a derived-event writer whose every write takes the delay, or never answers
// while the delay is hang, honouring the write's context either way.
type slowWriter struct {
	inner *lockedRecorder
	delay atomic.Int64
}

func (w *slowWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	d := time.Duration(w.delay.Load())
	if d != 0 {
		var after <-chan time.Time
		if d > 0 {
			after = time.After(d)
		}
		select {
		case <-after:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return w.inner.WriteMessages(ctx, msgs...)
}

func (w *slowWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *slowWriter) HandleResponse(error) {}

// useSlowWriter routes the rig's derived events through a writer the test can slow down.
func useSlowWriter(g *gapRig) *slowWriter {
	w := &slowWriter{inner: &lockedRecorder{}}
	g.rp.publisher = runtime.NewPublisher(w, g.rp.registry, g.metrics)
	return w
}

func (g *gapRig) failures(stage string) float64 {
	return testutil.ToFloat64(g.metrics.checkpointFailures.WithLabelValues(stage))
}

// A publish that never returns gives up at its deadline and is counted against the publish
// stage, not the save stage; the detections stay buffered for the retry.
func TestACheckpointWhosePublishHangsGivesUpAtItsDeadline(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	w := useSlowWriter(g)
	w.delay.Store(int64(hang))
	g.rp.cfg.CheckpointTimeout = 100 * time.Millisecond
	g.rp.handle(hot(t, 1, &fakeAck{}))
	if len(g.rp.pendingDets) == 0 {
		t.Fatal("the message raised no detection to publish")
	}

	done := make(chan bool, 1)
	go func() { done <- g.rp.checkpoint(context.Background()) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a checkpoint whose publish never returned reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("checkpoint is still blocked in the publish well past its deadline")
	}
	equal(t, "publish failures", g.failures(checkpointStagePublish), 1.0)
	equal(t, "save failures", g.failures(checkpointStageSave), 0.0)
	if len(g.rp.pendingDets) == 0 {
		t.Fatal("the failed publish dropped its detections")
	}
}

// A serialization error is counted against its own stage.
func TestACheckpointThatCannotSerializeCountsTheSerializeStage(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	g.rp.snapshotFn = func() ([]byte, error) { return nil, errors.New("cannot encode") }
	g.rp.handle(hot(t, 1, &fakeAck{}))
	if g.rp.checkpoint(context.Background()) {
		t.Fatal("a checkpoint that could not serialize reported success")
	}
	equal(t, "serialize failures", g.failures(checkpointStageSerialize), 1.0)
	equal(t, "save failures", g.failures(checkpointStageSave), 0.0)
}

// The publish and the save each get their own budget: a publish that eats most of its own
// deadline must not leave the save short, or be counted as a save failure.
func TestAPublishDoesNotSpendTheSavesBudget(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	w := useSlowWriter(g)
	store, _, delay := newSlowStore(t, 0)
	g.rp.Store = store
	g.rp.cfg.CheckpointTimeout = 400 * time.Millisecond
	w.delay.Store(int64(250 * time.Millisecond))
	delay.Store(int64(250 * time.Millisecond)) // 500ms together: past either deadline alone

	g.rp.handle(hot(t, 1, &fakeAck{}))
	if !g.rp.checkpoint(context.Background()) {
		t.Fatalf("a checkpoint whose two calls each fit their own deadline failed (publish=%v save=%v)",
			g.failures(checkpointStagePublish), g.failures(checkpointStageSave))
	}
}

// A save that is slower than the floor but working goes through once a slow one has been seen
// to complete, while one that hangs still gives up.
func TestTheSaveDeadlineAdaptsToASlowStoreButAHungOneStillGivesUp(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	store, _, delay := newSlowStore(t, 80*time.Millisecond)
	g.rp.Store = store
	g.rp.cfg.CheckpointTimeout = 150 * time.Millisecond

	// Cold: nothing observed, so the floor applies and a save at 80ms fits it.
	g.rp.handle(hot(t, 1, &fakeAck{}))
	if !g.rp.checkpoint(context.Background()) {
		t.Fatal("a save inside the floor failed")
	}
	if g.rp.lastSaveDuration < 80*time.Millisecond {
		t.Fatalf("the completed save was not observed: %v", g.rp.lastSaveDuration)
	}

	// The store slows past the floor (250ms > 150ms) but stays inside 4x what was seen.
	delay.Store(int64(250 * time.Millisecond))
	g.rp.handle(hot(t, 2, &fakeAck{}))
	if !g.rp.checkpoint(context.Background()) {
		t.Fatalf("a slow but working save failed although a slow one had been observed (deadline %v)", g.rp.saveTimeout())
	}

	// Truly hung: gives up, bounded by the adaptive deadline rather than hanging.
	delay.Store(int64(hang))
	g.rp.handle(hot(t, 3, &fakeAck{}))
	done := make(chan bool, 1)
	go func() { done <- g.rp.checkpoint(context.Background()) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a hung save reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a hung save was not given up on")
	}
	equal(t, "save failures", g.failures(checkpointStageSave), 1.0)
}

func TestTheSaveDeadlineIsFlooredAndCapped(t *testing.T) {
	rp := &ResolvedEventsProcessor{}
	if got := rp.saveTimeout(); got != defaultCheckpointTimeout {
		t.Fatalf("cold deadline = %v, want the floor %v", got, defaultCheckpointTimeout)
	}
	rp.lastSaveDuration = 20 * time.Second
	if got := rp.saveTimeout(); got != 4*20*time.Second {
		t.Fatalf("deadline = %v, want 4x the last save", got)
	}
	rp.lastSaveDuration = time.Hour
	if got := rp.saveTimeout(); got != maxSaveTimeout {
		t.Fatalf("deadline = %v, want the cap %v", got, maxSaveTimeout)
	}
	rp.cfg.CheckpointTimeout = 5 * time.Minute // an operator floor above the cap wins over it
	if got := rp.saveTimeout(); got != 5*time.Minute {
		t.Fatalf("deadline = %v, want the configured floor", got)
	}
}

// After a failed snapshot the next scheduled checkpoint does not serialize and send the whole
// payload again until the backoff has passed; a forced one (shutdown, tenant eviction) does.
func TestAFailedSnapshotIsNotRetriedEveryTick(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	store, entered, _ := newSlowStore(t, hang)
	g.rp.Store = store
	g.rp.cfg.CheckpointTimeout = 50 * time.Millisecond
	clock := newAtomicClock(testBase)
	g.rp.clock = clock
	g.rp.handle(hot(t, 1, &fakeAck{}))

	if g.rp.checkpoint(context.Background()) {
		t.Fatal("a hung save reported success")
	}
	attempts := entered.Load()
	if attempts == 0 {
		t.Fatal("the first checkpoint never reached the store")
	}

	clock.add(snapshotBackoffBase / 2)
	if g.rp.checkpoint(context.Background()) || entered.Load() != attempts {
		t.Fatal("the snapshot was re-sent inside the backoff")
	}
	g.rp.forceCheckpoint(context.Background())
	if entered.Load() == attempts {
		t.Fatal("a forced checkpoint was held back by the backoff")
	}
	attempts = entered.Load()

	clock.add(snapshotBackoffMax + time.Second) // past any backoff the failures so far reached
	g.rp.checkpoint(context.Background())
	if entered.Load() == attempts {
		t.Fatal("the snapshot was never retried once the backoff had passed")
	}
}

// resetForTerm starts the next term's gap park clock from zero; carried over, a brief failure
// early in the next term would trip the bound on the strength of the previous one.
func TestResetForTermClearsTheGapParkClock(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	g.rp.gapParkedSince = testBase
	g.rp.resetForTerm()
	if !g.rp.gapParkedSince.IsZero() {
		t.Fatal("the park clock survived into the next term")
	}
}

// With no supervisor to rebuild the term, a gap that cannot be read past its bound ends the
// process rather than leaving a Ready pod that detects nothing.
func TestAnUnleasedGapTripEndsTheProcess(t *testing.T) {
	g := newGapRig(t, &gatedOpener{})
	logged := observeProcessEnd(t, g.rp)
	clock := newAtomicClock(testBase)
	g.rp.clock = clock

	g.rp.handle(hot(t, 3, &fakeAck{}))
	clock.add(gapParkLimit + time.Second)
	g.rp.retryGapFill()
	requireProcessEnded(t, logged, g.rp.cfg.PartitionId, "could not be read")
}

// The stale-owner fence reads the committed sequence on the loop; an unanswered read must come
// back (as the "transient blip" it is documented to be) instead of holding the loop.
func TestTheStaleOwnerFenceGivesUpOnAReadThatNeverAnswers(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	store, _, _ := newSlowStore(t, hang)
	g.rp.Store = store
	g.rp.cfg.CheckpointTimeout = 100 * time.Millisecond

	done := make(chan bool, 1)
	go func() { done <- g.rp.detectStaleOwner(context.Background()) }()
	select {
	case stale := <-done:
		if stale {
			t.Fatal("an unanswered read was taken for proof of a split brain")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fence is still blocked reading the committed sequence well past its deadline")
	}
}
