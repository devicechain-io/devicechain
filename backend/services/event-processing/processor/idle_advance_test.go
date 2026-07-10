// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
)

// fakeProber is a controllable broker-emptiness signal for the idle-advance gate: pending is
// the undelivered backlog, ackPending is delivered-but-unacked (a peer's in-flight during a
// rolling-update overlap), both 0 = caught up; err simulates an unreachable broker / gone
// consumer.
type fakeProber struct {
	pending    uint64
	ackPending uint64
	err        error
}

func (p *fakeProber) Backlog(context.Context) (uint64, uint64, error) {
	return p.pending, p.ackPending, p.err
}

// absenceReg wires a registry with a single absence rule (30s dead-man timeout) for tenant
// acme / profile p@1, keyed by device — the rule idle-advance exists to fire on silence.
func absenceReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	cr, err := rules0.Compile(rules0.Rule{
		ID:   "acme/dead",
		Name: "dead",
		Type: rules0.TypeAbsence,
		Ttl:  rules0.Duration(30 * time.Second),
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile absence: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

// absenceProcessor builds a restored processor over the absence registry with a controllable
// clock, a caught-up broker prober, and a 10s checkpoint interval. Tests drive idleAdvance
// directly (TickInterval is pushed far out); each sets lastLiveRead/lastCheckpoint to old
// values so the quiet-guard and interval gates pass, then flips one gate to prove suppression.
func absenceProcessor(t *testing.T, clock detectcore.Clock, w *captureWriter, guard time.Duration) *ResolvedEventsProcessor {
	t.Helper()
	store := newTestStore(t)
	reg := absenceReg(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   100,
			CheckpointInterval: 10 * time.Second,
			TickInterval:       time.Hour,
			Lateness:           0,
			IdleAdvanceGuard:   guard,
			Clock:              clock,
		},
		registry:     reg,
		publisher:    runtime.NewPublisher(w, reg, (*detectMetrics)(nil)),
		clock:        clock,
		procCtx:      ctx,
		procCancel:   cancel,
		backlogProbe: &fakeProber{}, // caught up (pending 0, ackPending 0) by default
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp
}

// openGates sets lastLiveRead and lastCheckpoint far enough in the past that the quiet-guard
// and interval gates both pass at clock time `now`.
func openGates(rp *ResolvedEventsProcessor, now time.Time) {
	rp.lastLiveRead = now.Add(-time.Hour)
	rp.lastCheckpoint = now.Add(-time.Hour)
}

// A device reports once (arming its absence timer), then goes silent. With the broker confirmed
// caught up, the wall-clock idle advance fires the absence, publishes a derived event, and
// commits the consumed-timer state. The committed sequence stays at the heartbeat's.
func TestIdleAdvanceFiresAbsenceOnSilence(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{})) // deadline testBase+31s
	if w.writes != 0 {
		t.Fatalf("a heartbeat must not itself fire absence; writes=%d", w.writes)
	}

	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 1 {
		t.Fatalf("idle-advance should publish exactly one absence derived event; got %d", w.writes)
	}
	snap, ok, err := rp.Store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("idle-advance firing should commit a snapshot; ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 1 {
		t.Fatalf("idle-advance must not change the committed sequence; got %d, want 1", snap.StreamSeq)
	}
	if want := testBase.Add(40 * time.Second); !snap.Watermark.Equal(want) {
		t.Fatalf("committed watermark = %v, want %v (now-lateness)", snap.Watermark, want)
	}
}

// A real backlog (NumPending > 0) means the tail is NOT drained — even though the read loop is
// quiet — so idle-advance must suppress rather than fire over the unconsumed heartbeats. This
// is the outage/pause hole the review found in the naive silence-only gate.
func TestIdleAdvanceSuppressedWhenBacklogPending(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)
	rp.backlogProbe = &fakeProber{pending: 3} // broker still has undelivered messages

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance must suppress while a backlog is pending; writes=%d", w.writes)
	}
	if !rp.engine.Watermark().Equal(testBase.Add(1 * time.Second)) {
		t.Fatalf("suppressed idle-advance must not move the frontier; got %v", rp.engine.Watermark())
	}
}

// If the prober cannot reach the broker / consumer (error), idle-advance fails safe and
// suppresses rather than advancing blind.
func TestIdleAdvanceSuppressedOnProbeError(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)
	rp.backlogProbe = &fakeProber{err: errors.New("broker unreachable")}

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance must fail safe on a probe error; writes=%d", w.writes)
	}
}

// A nil prober (a reader that cannot report NumPending) disables idle-advance entirely — the
// fail-safe posture, so absence-on-silence is never fired blind.
func TestIdleAdvanceSuppressedWithoutProber(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)
	rp.backlogProbe = nil

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("a nil prober must disable idle-advance; writes=%d", w.writes)
	}
}

// The quiet-guard gate holds idle-advance off while the read loop was recently active (its
// local fetch buffer may still hold undelivered messages).
func TestIdleAdvanceSuppressedWhenNotQuiet(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	rp.lastCheckpoint = now.Add(-time.Hour)
	rp.lastLiveRead = now.Add(-2 * time.Second) // read 2s ago < 5s guard
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance must be suppressed within the quiet guard; writes=%d", w.writes)
	}
}

// The interval gate holds idle-advance off until a checkpoint interval has elapsed, so the
// frontier only moves at commit points.
func TestIdleAdvanceSuppressedWithinInterval(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	rp.lastLiveRead = now.Add(-time.Hour)
	rp.lastCheckpoint = now.Add(-2 * time.Second) // < 10s interval
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance must hold to the checkpoint interval; writes=%d", w.writes)
	}
}

// A non-positive guard disables idle-advance entirely (the operator opt-out).
func TestIdleAdvanceDisabled(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 0) // disabled

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(time.Hour)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("disabled idle-advance must fire nothing; writes=%d", w.writes)
	}
}

// A peer consuming heartbeats during a rolling-update overlap holds them as delivered-but-
// unacked (ackPending) — invisible to the pending count. idle-advance must suppress so it does
// not fire a fabricated absence for a device the peer is actively serving.
func TestIdleAdvanceSuppressedWhenAckPending(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)
	rp.backlogProbe = &fakeProber{ackPending: 2} // a peer holds delivered-unacked messages

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance must suppress while a peer holds ack-pending messages; writes=%d", w.writes)
	}
}

// With nothing armed to fire (no timer, no open window), idle-advance does nothing at all — it
// must NOT advance or checkpoint a bare watermark move, so a fully idle engine stays quiet
// instead of writing a snapshot every interval forever. A later event re-derives the frontier
// itself, so leaving it at rest is replay-safe.
func TestIdleAdvanceSkipsWhenNoPendingWork(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second) // no heartbeat: no armed timer

	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("idle-advance with no pending work publishes nothing; writes=%d", w.writes)
	}
	if _, ok, _ := rp.Store.Load(ctx, "singleton"); ok {
		t.Fatal("idle-advance with no pending work must not write a snapshot")
	}
	if !rp.engine.Watermark().Equal(time.Time{}) {
		t.Fatalf("idle-advance with no pending work must not move the frontier; got %v", rp.engine.Watermark())
	}
}

// idle-advance detections are NOT replayable, so a broker outage on their delivery must DEFER
// the whole checkpoint (deliver-before-checkpoint): the fired detection is retained, no snapshot
// commits, state stays dirty, and idleUncommitted latches so the loop parks live-event
// application until a checkpoint commits. Once the broker recovers, the retry commits and clears
// the park.
func TestIdleAdvanceDefersAndParksOnPublishError(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{err: errors.New("broker down")}
	rp := absenceProcessor(t, clock, w, 5*time.Second)

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if len(rp.pendingDets) != 1 {
		t.Fatalf("a fired detection must be retained when publish fails; pendingDets=%d", len(rp.pendingDets))
	}
	if _, ok, _ := rp.Store.Load(ctx, "singleton"); ok {
		t.Fatal("no snapshot may commit while the fired detection could not be delivered")
	}
	if !rp.dirty || !rp.idleUncommitted {
		t.Fatalf("a deferred idle-advance must stay dirty and park: dirty=%v idleUncommitted=%v", rp.dirty, rp.idleUncommitted)
	}

	// Broker recovers: the next checkpoint delivers the detection, commits, and clears the park.
	w.err = nil
	rp.checkpoint(ctx)
	if rp.idleUncommitted || rp.dirty {
		t.Fatalf("a committed checkpoint must clear the park: dirty=%v idleUncommitted=%v", rp.dirty, rp.idleUncommitted)
	}
	if w.writes != 1 {
		t.Fatalf("recovery should publish the retained detection exactly once; writes=%d", w.writes)
	}
	if _, ok, _ := rp.Store.Load(ctx, "singleton"); !ok {
		t.Fatal("recovery should commit the snapshot")
	}
}

// If another writer has committed a checkpoint AHEAD of this engine (a split-brain rolling-
// update overlap), idle-advance fences BEFORE emitting: it must not publish a wall-clock-
// fabricated absence onto the shared feed. The losing writer halts (stale) and emits nothing.
func TestIdleAdvanceFencesStaleOwner(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	rp := absenceProcessor(t, clock, w, 5*time.Second)

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{})) // our applied seq = 1
	// A peer (the true owner) committed a higher checkpoint.
	if err := rp.Store.Save(ctx, &model.DetectSnapshot{
		PartitionId: "singleton", StreamSeq: 100, Watermark: testBase.Add(90 * time.Second), Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed leading snapshot: %v", err)
	}
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if w.writes != 0 {
		t.Fatalf("a stale writer must not publish a fabricated absence; writes=%d", w.writes)
	}
	if !rp.stale {
		t.Fatal("detecting a higher committed sequence must latch this writer stale")
	}
	if len(rp.pendingDets) != 0 {
		t.Fatalf("a fenced idle-advance must buffer no detections; got %d", len(rp.pendingDets))
	}
}
