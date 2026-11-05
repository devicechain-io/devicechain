// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// absenceProcessorK is absenceProcessor at k detection shards.
func absenceProcessorK(t *testing.T, k int, clock detectcore.Clock, w *captureWriter) *ResolvedEventsProcessor {
	t.Helper()
	store := newTestStore(t)
	reg := absenceReg(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId: "singleton", CheckpointEvents: 100, CheckpointInterval: 10 * time.Second,
			TickInterval: time.Hour, Lateness: 0, IdleAdvanceGuard: 5 * time.Second, Clock: clock, Shards: k,
		},
		registry:     reg,
		publisher:    runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:        clock,
		procCtx:      ctx,
		procCancel:   cancel,
		backlogProbe: &fakeProber{},
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(rp.closeDetector)
	return rp
}

// The wall-clock idle advance reaches a concurrent engine: it fires the silent device's absence,
// the derived event is published, and the committed snapshot is the single engine's.
func TestIdleAdvanceOnConcurrentShardsMatchesTheSingleEngine(t *testing.T) {
	ctx := context.Background()
	run := func(k int) (*captureWriter, []byte) {
		clock := detectcore.NewManualClock(testBase)
		w := &captureWriter{}
		rp := absenceProcessorK(t, k, clock, w)
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		rp.metrics = NewDetectMetrics(ms)
		for i, dev := range []string{"d1", "d2", "d3", "d4", "d5"} {
			rp.handle(measuredMsg(t, uint64(i+1), "acme", dev, "p@1", "temperature", "20", &fakeAck{}))
		}
		now := testBase.Add(40 * time.Second)
		openGates(rp, now)
		clock.Set(now)
		rp.idleAdvance(ctx, now)
		snap, ok, err := rp.Store.Load(ctx, "singleton")
		if err != nil || !ok {
			t.Fatalf("k=%d: idle-advance firing should commit a snapshot; ok=%v err=%v", k, ok, err)
		}
		if got := testutil.ToFloat64(rp.metrics.idleDetectionsTotal); got != 5 {
			t.Fatalf("k=%d: the idle advance counted %v detections, want 5", k, got)
		}
		return w, snap.Payload
	}
	cw, cs := run(1)
	if cw.writes != 5 {
		t.Fatalf("the control run published %d absences, want 5", cw.writes)
	}
	for _, k := range []int{2, 4} {
		w, s := run(k)
		if w.writes != cw.writes {
			t.Fatalf("k=%d published %d absences, the single engine %d", k, w.writes, cw.writes)
		}
		if !bytes.Equal(s, cs) {
			t.Fatalf("k=%d committed a different snapshot from the single engine's", k)
		}
	}
}

// Detections the shards made for a tenant, and the buffer has not taken in yet, are dropped with
// the tenant's other buffered detections: a purge that left them in the shards would publish them
// at the next checkpoint, after the tenant was reported gone.
func TestTenantPurgeDropsDetectionsStillInTheShards(t *testing.T) {
	ctx := context.Background()
	rp := shardedProcessor(t, ctx, newTestStore(t), &valueCapture{}, 4, false)
	defer rp.closeDetector()
	for i := uint64(1); i <= 80; i++ {
		rp.handle(shardedMessage(t, i))
	}
	if len(rp.pendingDets) != 0 {
		t.Fatalf("%d detections were buffered before the purge: the test needs them to be still in the shards", len(rp.pendingDets))
	}
	n := rp.dropTenantBuffers(func(id string) bool { return strings.HasPrefix(id, "acme/") }, "acme")
	if n == 0 {
		t.Fatal("the purge dropped nothing: the shards' detections were not part of it")
	}
	rp.syncDetector()
	for _, d := range rp.pendingDets {
		if strings.HasPrefix(d.RuleID, "acme/") {
			t.Fatalf("a detection of the purged tenant survived: %s/%s", d.RuleID, d.Series)
		}
	}
}

// The tick takes in what the shards have made and publishes what is ready ahead of the checkpoint,
// as it does for the single engine: a threshold spike is early-publishable, so it is written before
// any checkpoint has run.
func TestTickPublishesReadyDetectionsEarlyOnConcurrentShards(t *testing.T) {
	ctx := context.Background()
	w := &valueCapture{}
	rp := shardedProcessor(t, ctx, newTestStore(t), w, 4, false)
	defer rp.closeDetector()
	for i := uint64(1); i <= 30; i++ {
		rp.handle(shardedMessage(t, i))
	}
	rp.pipelined().Sync() // the shards have applied everything; the tick finds it ready
	if len(rp.pendingDets) != 0 {
		t.Fatalf("%d detections were buffered before the tick", len(rp.pendingDets))
	}
	rp.collectDetections()
	if len(rp.pendingDets) == 0 {
		t.Fatal("the tick took in no detections")
	}
	rp.flushDetections(ctx)
	if got := len(w.sorted()); got == 0 {
		t.Fatal("the tick published nothing ahead of the checkpoint")
	}
	if !strings.Contains(strings.Join(w.sorted(), "\n"), "acme/spike") {
		t.Fatal("no spike was published by the tick, though spikes are early-publishable")
	}
}

// A detection a wall-clock advance makes is never published early: only what replays identically
// from the log may go ahead of the checkpoint, and an advance is not in the log.
func TestWallClockAdvanceDetectionsAreNeverPublishedEarlyOnConcurrentShards(t *testing.T) {
	clock := detectcore.NewManualClock(testBase)
	rp := absenceProcessorK(t, 4, clock, &captureWriter{})
	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	rp.pipelined().Sync()
	rp.tagNext(testBase.Add(time.Minute), false)
	if !rp.engine.Advance(testBase.Add(time.Minute)) {
		t.Fatal("the advance fired nothing")
	}
	rp.collectDetections()
	if len(rp.pendingDets) == 0 {
		t.Fatal("the advance's detection was not taken in")
	}
	for _, d := range rp.pendingDets {
		if d.early {
			t.Fatalf("a wall-clock detection is marked early: %s/%s", d.RuleID, d.Series)
		}
		if !d.triggeredAt.Equal(testBase.Add(time.Minute)) {
			t.Fatalf("a wall-clock detection is stamped %s, want the advance's time", d.triggeredAt)
		}
	}
}

// absorb stamps each detection with the tag it was made under, publishes early only what was made
// from the log, and marks the loop dirty so a checkpoint is certain to flush it.
func TestAbsorbKeepsTheShardsStampsAndTheReplayableFlag(t *testing.T) {
	rp := shardedProcessor(t, context.Background(), newTestStore(t), &valueCapture{}, 2, false)
	at := testBase.Add(time.Hour)
	spike := detectcore.Detection{RuleID: "acme/spike", Series: "dev-01", Kind: detectcore.Threshold, At: at}
	rp.dirty = false
	n := rp.absorb([]detectcore.TaggedDetection{
		{Detection: spike, Tag: detectcore.Tag{At: at, Replayable: true}},
		{Detection: spike, Tag: detectcore.Tag{At: at.Add(time.Second), Replayable: false}},
	})
	if n != 2 || !rp.dirty {
		t.Fatalf("absorbed %d, dirty=%v", n, rp.dirty)
	}
	if got := rp.pendingDets; !got[0].early || got[1].early {
		t.Fatalf("early flags %v %v: only the replayable one may go ahead of the checkpoint", got[0].early, got[1].early)
	}
	if !rp.pendingDets[1].triggeredAt.Equal(at.Add(time.Second)) {
		t.Fatalf("stamp %s was not kept", rp.pendingDets[1].triggeredAt)
	}
}

// The invariant counter and the barrier histogram are wired to the observer the engine is given.
func TestShardObserverFeedsTheSeries(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := NewDetectMetrics(ms)
	m.InvariantViolation()
	m.InvariantViolation()
	if got := testutil.ToFloat64(m.shard.violations); got != 2 {
		t.Fatalf("violations = %v, want 2", got)
	}
}

// A rule update takes in the detections the shards have made first, so that whether each may be
// published early is judged from the rule it was made under and not from its replacement.
func TestRuleUpdateTakesInTheShardsDetectionsFirst(t *testing.T) {
	ctx := context.Background()
	rp := shardedProcessor(t, ctx, newTestStore(t), &valueCapture{}, 4, false)
	for i := uint64(1); i <= 40; i++ {
		rp.handle(shardedMessage(t, i))
	}
	if len(rp.pendingDets) != 0 {
		t.Fatalf("%d detections were buffered before the update", len(rp.pendingDets))
	}
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{lateRule(t)}})
	if len(rp.pendingDets) == 0 {
		t.Fatal("the rule update did not take in the detections the shards had already made")
	}
}

// A detection made by an operation with no message behind it (a dead-man disarm resolves at the
// watermark) carries no stamp. absorb stamps it with the loop's clock at the moment it takes it in,
// which is a known difference from the single engine, whose next drain's time it would get. The
// time is not part of the detection's identity (DedupID), and such a detection is never published
// early. This pins both.
func TestAZeroTagDetectionIsStampedWhenTakenInAndNeverPublishedEarly(t *testing.T) {
	rp := shardedProcessor(t, context.Background(), newTestStore(t), &valueCapture{}, 2, false)
	now := testBase.Add(3 * time.Hour)
	rp.clock = detectcore.NewManualClock(now)
	spike := detectcore.Detection{RuleID: "acme/spike", Series: "dev-01", Kind: detectcore.Threshold, At: testBase}
	rp.absorb([]detectcore.TaggedDetection{{Detection: spike}, {Detection: spike}})
	if len(rp.pendingDets) != 2 {
		t.Fatalf("absorbed %d, want 2", len(rp.pendingDets))
	}
	for i, d := range rp.pendingDets {
		if !d.triggeredAt.Equal(now) {
			t.Fatalf("detection %d is stamped %s, want the clock at intake %s", i, d.triggeredAt, now)
		}
		if d.early {
			t.Fatalf("detection %d with no stamp is early-publishable", i)
		}
	}
}

// A dead-man disarm queues its Resolved in the engine and no message follows it: a checkpoint, and
// the term-end flush, must still deliver it before the snapshot that carries the cleared latch. With
// concurrent shards the disarm may still be in a shard's queue, so the flush waits for them first.
func TestADisarmWithNoMessageIsPublishedByACheckpointAndByTheFinalFlushOnConcurrentShards(t *testing.T) {
	for _, final := range []bool{false, true} {
		final := final
		name := "checkpoint"
		if final {
			name = "final flush"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			w := &valueCapture{}
			rp := shardedProcessor(t, ctx, newTestStore(t), w, 4, false)
			key := detectcore.SeriesKey{Rule: "acme/silent", Series: "silent-0"}
			// The roster device never reported: advance past its 40s dead-man so the absence raises.
			rp.tagNext(testBase.Add(10*time.Minute), false)
			if !rp.engine.Advance(testBase.Add(10 * time.Minute)) {
				t.Fatal("the advance fired nothing")
			}
			rp.dirty = true
			if !rp.checkpoint(ctx) {
				t.Fatal("the checkpoint publishing the raise deferred")
			}
			before := len(w.laneOrdered())
			if before == 0 {
				t.Fatal("setup: nothing was raised")
			}
			// The disarm: no message after it, nothing else dirty, so only the drain can deliver it.
			rp.engine.RemoveExpected(key)
			rp.dirty = false
			if final {
				rp.finalCheckpoint()
			} else {
				rp.dirty = true
				if !rp.checkpoint(ctx) {
					t.Fatal("the checkpoint deferred")
				}
			}
			found := false
			for _, v := range w.laneOrdered() {
				if strings.Contains(v, "silent-0") && strings.Contains(v, `"edge":"resolved"`) {
					found = true
				}
			}
			if !found {
				t.Fatalf("the disarm's Resolved was not published (%d values before, %d after)", before, len(w.laneOrdered()))
			}
		})
	}
}
