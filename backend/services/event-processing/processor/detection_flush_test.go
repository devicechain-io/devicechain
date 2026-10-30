// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A detection must reach the broker within a tick of firing, not wait out the checkpoint interval.
// The interval here is an hour and the clock never moves, so only the tick's own early flush can
// publish it. A device reporting every few seconds sees the interval as alarm-to-command latency
// otherwise: measured at 0.5 to 9.8 s, uniformly, on a quiet single-device cluster with the 10 s
// default.
//
// It also pins what the early flush must NOT do: commit a snapshot or ack. Those stay with the
// checkpoint, which is what keeps the committed sequence behind every unpublished detection.
func TestADetectionIsPublishedWithinATickNotAtTheCheckpoint(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	ack := &fakeAck{}
	before, _, err := g.rp.Store.LoadCommittedSeq(context.Background(), "singleton")
	if err != nil {
		t.Fatalf("load committed seq: %v", err)
	}
	// The loop's last checkpoint is the test clock's now, so the hour is a real wait.
	g.rp.lastCheckpoint = testBase
	startLoop(t, g, newAtomicClock(testBase),
		&feedReader{fakeReader: fakeReader{results: []readResult{{msg: hot(t, 1, ack)}}}})

	waitFor(t, "the detection to be published before any checkpoint", func() bool {
		return len(g.rec.published()) == 1
	})
	// Give the loop several more ticks to do what it must not.
	time.Sleep(50 * time.Millisecond)
	if after, _, err := g.rp.Store.LoadCommittedSeq(context.Background(), "singleton"); err != nil || after != before {
		t.Fatalf("the early flush moved the committed sequence from %d to %d (err=%v)", before, after, err)
	}
	if got := len(g.rec.published()); got != 1 {
		t.Fatalf("the detection was published %d times, want once", got)
	}
}

// flushRig is a gap rig over the given compiled rules with an empty attribute view and a frozen
// clock, with nothing run on the loop: a test drives handle, the tick's flushDetections and the
// checkpoint by hand.
func flushRig(t *testing.T, scoped ...runtime.ScopedRule) *gapRig {
	t.Helper()
	g := newGapRigWith(t, &fakeReplayOpener{}, runtime.NewRuleRegistry(scoped))
	g.rp.attrView = runtime.NewDeviceAttributeView()
	g.rp.clock = newAtomicClock(testBase)
	g.rp.lastCheckpoint = testBase
	return g
}

func compiledThreshold(t *testing.T, id string, cond rules0.Condition) runtime.ScopedRule {
	t.Helper()
	cr, err := rules0.Compile(rules0.Rule{ID: id, Name: id, Type: rules0.TypeThreshold, When: cond}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile %s: %v", id, err)
	}
	return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}
}

func staticRule(t *testing.T) runtime.ScopedRule {
	thr := 80.0
	return compiledThreshold(t, "acme/hot", rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr})
}

func dynamicThresholdRule(t *testing.T) runtime.ScopedRule {
	return compiledThreshold(t, "acme/dyn", rules0.Condition{Metric: "temperature", Op: rules0.OpGt, ThresholdAttr: "lim"})
}

// A static threshold is replay-deterministic, so the tick publishes it ahead of the checkpoint.
func TestAStaticThresholdDetectionIsFlushedOnTheTick(t *testing.T) {
	g := flushRig(t, staticRule(t))
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.flushDetections(context.Background())
	equal(t, "published on the tick", g.rec.published(), series(1))
	g.commit(t)
	equal(t, "published once in all", g.rec.published(), series(1))
}

// A dynamic (attribute) threshold is evaluated against the attribute as it is now, so a replay
// need not reproduce it: it waits for the checkpoint. Without the classification (every detection
// flushed) this fails at the first assertion.
func TestADynamicThresholdDetectionWaitsForTheCheckpoint(t *testing.T) {
	g := flushRig(t, dynamicThresholdRule(t))
	g.rp.attrView.ReplaceDevice("acme", "dev-1", []runtime.AttrEntry{
		{Tenant: "acme", DeviceToken: "dev-1", Scope: "SERVER", Key: "lim", Value: 80}})
	g.rp.handle(hot(t, 1, &fakeAck{}))
	if len(g.rp.pendingDets) != 1 {
		t.Fatalf("the dynamic rule did not fire: %d buffered", len(g.rp.pendingDets))
	}
	g.rp.flushDetections(context.Background())
	equal(t, "published on the tick", g.rec.published(), series())
	g.commit(t)
	equal(t, "published at the checkpoint", g.rec.published(), series(1))
}

// A detection that dropSupersededDetections judges at publish time (here an absence) waits for
// the checkpoint, however it was drained.
func TestAGatedDetectionWaitsForTheCheckpoint(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	w := &edgeWriter{}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	rp.clock = newAtomicClock(testBase)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)
	eng.Advance(testBase.Add(11 * time.Second))
	// Drained as an event-driven batch would be: the kind, not the source, must keep it back.
	rp.drainDetections(testBase.Add(11*time.Second), true)
	if len(rp.pendingDets) != 1 {
		t.Fatalf("the absence did not fire: %d buffered", len(rp.pendingDets))
	}
	rp.flushDetections(context.Background())
	if len(w.got) != 0 {
		t.Fatalf("a gated detection was flushed early: %v", w.got)
	}
	if !rp.publishPending(context.Background()) || len(w.got) != 1 {
		t.Fatalf("the checkpoint did not publish it: %v", w.got)
	}
}

// Every kind is decided on purpose, and one nobody has decided is held for the checkpoint.
func TestOnlyDecidedKindsArePublishedEarly(t *testing.T) {
	for _, tc := range []struct {
		kind  detectcore.RuleKind
		attrs bool
		want  bool
	}{
		{detectcore.Threshold, false, true},
		{detectcore.Repeating, false, true},
		{detectcore.Threshold, true, false},
		{detectcore.Repeating, true, false},
		{detectcore.Absence, false, false},
		{detectcore.Duration, false, false},
		{detectcore.Session, false, false},
		{detectcore.Aggregate, false, false},
		{detectcore.DeltaRate, false, false},
		{detectcore.CountWindow, false, false},
		{detectcore.SlidingAgg, false, false},
		{detectcore.Correlation, false, false},
		{detectcore.Connectivity, false, false},
		{detectcore.RuleKind(1000), false, false}, // a kind added after this was written
	} {
		if got := earlyPublishableKind(tc.kind, tc.attrs); got != tc.want {
			t.Errorf("kind %d (reads attributes %v): early = %v, want %v", tc.kind, tc.attrs, got, tc.want)
		}
	}
	// And a rule the registry does not know holds nothing early.
	g := flushRig(t, staticRule(t))
	if g.rp.earlyPublishable(detectcore.Detection{RuleID: "acme/unknown", Kind: detectcore.Threshold}) {
		t.Error("a detection of an unknown rule was marked early")
	}
}

// failingWriter refuses every write and counts them.
type failingWriter struct{ calls atomic.Int32 }

func (w *failingWriter) WriteMessages(context.Context, ...messaging.Message) error {
	w.calls.Add(1)
	return errors.New("broker unavailable")
}

func (w *failingWriter) WriteToDevice(ctx context.Context, _ string, m ...messaging.Message) error {
	return w.WriteMessages(ctx, m...)
}

func (w *failingWriter) HandleResponse(error) {}

// A broker that refuses costs one attempt, not one per tick, until a checkpoint has run.
func TestAFailedFlushIsNotRetriedOnTheNextTicks(t *testing.T) {
	g := flushRig(t, staticRule(t))
	w := &failingWriter{}
	g.rp.publisher = runtime.NewPublisher(w, g.rp.registry, g.metrics)
	g.rp.handle(hot(t, 1, &fakeAck{}))

	g.rp.flushDetections(context.Background())
	first := w.calls.Load()
	if first == 0 {
		t.Fatal("the first flush never reached the writer")
	}
	for range 3 {
		g.rp.flushDetections(context.Background())
	}
	if got := w.calls.Load(); got != first {
		t.Fatalf("the tick retried a failed flush: %d calls, want %d", got, first)
	}
	if len(g.rp.pendingDets) != 1 {
		t.Fatalf("the failed detection left the buffer: %d", len(g.rp.pendingDets))
	}
	// A checkpoint that ran clears the hold.
	g.rp.clock.(*atomicClock).add(time.Second)
	g.rp.lastCheckpoint = g.rp.clock.Now()
	g.rp.flushDetections(context.Background())
	if w.calls.Load() == first {
		t.Fatal("the flush was not retried after a checkpoint")
	}
}

// A replica that no longer holds the partition publishes nothing early.
func TestNothingIsFlushedOutsideATerm(t *testing.T) {
	g := flushRig(t, staticRule(t))
	g.rp.Lease = &messaging.DistributedLease{}
	g.rp.Gate = NewTermGate() // closed
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.flushDetections(context.Background())
	equal(t, "published with the gate closed", g.rec.published(), series())

	g.rp.Gate.Enter(func() bool { return true })
	g.rp.flushDetections(context.Background())
	equal(t, "published with the gate open", g.rec.published(), series(1))
}
