// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// carryActionWriter drives real REACT dispatch from the processor's published output.
type carryActionWriter struct {
	t     *testing.T
	rd    *ReactDispatcher
	edges []runtime.DerivedEvent
}

func (w *carryActionWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	for _, m := range msgs {
		var de runtime.DerivedEvent
		if err := json.Unmarshal(m.Value, &de); err != nil {
			return err
		}
		w.edges = append(w.edges, de)
		w.rd.handle(derivedMsg(w.t, "acme", de, 0, &fakeAck{}))
	}
	return nil
}
func (w *carryActionWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (*carryActionWriter) HandleResponse(error) {}

func publishCarryProc(t *testing.T, kind dc.RuleKind) (*ResolvedEventsProcessor, *carryActionWriter, *reactFakeSink) {
	t.Helper()
	v1 := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, _ := armedProcessor(t, v1)
	sr := absScopedProc("acme", "p1@v1", v1)
	sr.Compiled.Core.Kind = kind
	if kind == dc.Threshold {
		sr.Compiled.Type = "threshold"
	}
	rp.registry = runtime.NewRuleRegistry([]runtime.ScopedRule{sr})
	rp.engine = dc.NewEngine(rp.registry.Cores(), 0)
	rp.armer = runtime.NewDeadmanArmer(rp.registry, rp.engine)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase}, nil)
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)
	sink := &reactFakeSink{}
	w := &carryActionWriter{t: t, rd: newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, sink)}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	return rp, w, sink
}
func carryFlush(t *testing.T, rp *ResolvedEventsProcessor, sec int) {
	t.Helper()
	rp.drainDetections(testBase.Add(time.Duration(sec)*time.Second), true)
	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
}
func carryPublish(rp *ResolvedEventsProcessor, kind dc.RuleKind, version, token string) {
	id := runtime.ComposeRuleID("acme", "p1@"+version+"/"+token)
	sr := absScopedProc("acme", "p1@"+version, id)
	sr.Compiled.Core.Kind = kind
	if kind == dc.Threshold {
		sr.Compiled.Type = "threshold"
	}
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{sr}, active: &runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@" + version, PublishedAt: testBase.Add(20 * time.Second)}})
}
func carryReading(rp *ResolvedEventsProcessor, seq uint64, version string, sec int, match bool) {
	rp.engine.ProcessEvent(dc.Event{Seq: seq, Key: dc.SeriesKey{Rule: runtime.ComposeRuleID("acme", "p1@"+version+"/r1"), Series: "dev1"}, Time: testBase.Add(time.Duration(sec) * time.Second), Match: match})
}

func TestPublishThresholdKeepsOneCommand(t *testing.T) {
	rp, w, sink := publishCarryProc(t, dc.Threshold)
	carryReading(rp, 1, "v1", 1, true)
	carryFlush(t, rp, 1)
	carryPublish(rp, dc.Threshold, "v2", "r1")
	carryReading(rp, 2, "v2", 21, true)
	carryFlush(t, rp, 21)
	if len(sink.sent) != 1 {
		t.Fatalf("publish re-sent commands: got %d, want 1", len(sink.sent))
	}
	if len(w.edges) != 1 {
		t.Fatalf("publish emitted extra edges: %+v", w.edges)
	}
	carryReading(rp, 3, "v2", 22, false)
	carryFlush(t, rp, 22)
	if len(w.edges) != 2 || w.edges[1].Edge != runtime.EdgeResolved {
		t.Fatalf("healthy successor must clear: %+v", w.edges)
	}
	carryReading(rp, 4, "v2", 23, true)
	carryFlush(t, rp, 23)
	if len(sink.sent) != 2 || w.edges[2].RuleID != "acme/p1@v2/r1" {
		t.Fatalf("next rising edge must fire v2: %+v", w.edges)
	}
}

func TestPublishAbsenceKeepsOneCommand(t *testing.T) {
	rp, w, sink := publishCarryProc(t, dc.Absence)
	rp.engine.Advance(testBase.Add(11 * time.Second))
	carryFlush(t, rp, 11)
	carryPublish(rp, dc.Absence, "v2", "r1")
	rp.engine.Advance(testBase.Add(31 * time.Second))
	carryFlush(t, rp, 31)
	if len(sink.sent) != 1 {
		t.Fatalf("publish re-sent commands: got %d, want 1", len(sink.sent))
	}
	if len(w.edges) != 1 {
		t.Fatalf("publish emitted extra edges: %+v", w.edges)
	}
	carryReading(rp, 1, "v2", 32, true)
	carryFlush(t, rp, 32)
	if len(w.edges) != 2 || w.edges[1].Edge != runtime.EdgeResolved {
		t.Fatalf("heartbeat must clear: %+v", w.edges)
	}
}

func TestPublishLateRaiseClearsOnlyOldLatch(t *testing.T) {
	rp, w, sink := publishCarryProc(t, dc.Threshold)
	carryReading(rp, 1, "v1", 1, true)
	carryFlush(t, rp, 1)
	rp.dirty = false
	carryPublish(rp, dc.Threshold, "v2", "r1")
	if !rp.dirty {
		t.Fatal("carry must dirty the checkpoint")
	}
	carryReading(rp, 2, "v1", 21, true)
	carryFlush(t, rp, 21)
	if len(sink.sent) != 1 || len(w.edges) != 1 {
		t.Fatalf("late raise published: %+v", w.edges)
	}
	// A dropped old raise must lose its latch. It owes no subsequent clear.
	carryReading(rp, 3, "v1", 22, false)
	carryFlush(t, rp, 22)
	if len(w.edges) != 1 {
		t.Fatalf("dropped raise left a latch: %+v", w.edges)
	}
	carryReading(rp, 4, "v2", 23, true)
	carryFlush(t, rp, 23)
	if len(sink.sent) != 1 {
		t.Fatal("late gate cleared successor latch")
	}
	carryReading(rp, 5, "v2", 24, false)
	carryFlush(t, rp, 24)
	if len(w.edges) != 2 || w.edges[1].Edge != runtime.EdgeResolved {
		t.Fatalf("successor latch lost: %+v", w.edges)
	}
}

func TestPublishNewRuleTokenStillRaises(t *testing.T) {
	rp, _, sink := publishCarryProc(t, dc.Threshold)
	carryReading(rp, 1, "v1", 1, true)
	carryFlush(t, rp, 1)
	carryPublish(rp, dc.Threshold, "v2", "new-rule")
	rp.engine.ProcessEvent(dc.Event{Seq: 2, Key: dc.SeriesKey{Rule: "acme/p1@v2/new-rule", Series: "dev1"}, Time: testBase.Add(21 * time.Second), Match: true})
	carryFlush(t, rp, 21)
	if len(sink.sent) != 2 {
		t.Fatalf("new token must fire independently, got %d commands", len(sink.sent))
	}
}

func TestPublishCarryRestart(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-handoff-checkpoint", true: "after-handoff-checkpoint"}[checkpoint], func(t *testing.T) {
			rp, w, sink := publishCarryProc(t, dc.Threshold)
			carryReading(rp, 1, "v1", 1, true)
			carryFlush(t, rp, 1)
			snap, err := rp.engine.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			carryPublish(rp, dc.Threshold, "v2", "r1")
			if checkpoint {
				snap, err = rp.engine.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
			}
			eng, err := dc.RestoreSharded(4, rp.registry.Cores(), 0, snap)
			if err != nil {
				t.Fatal(err)
			}
			rp.engine = eng
			rp.armer = runtime.NewDeadmanArmer(rp.registry, eng)
			rosters := []runtime.RosterEntry{{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}}
			actives := []runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase.Add(20 * time.Second)}}
			rp.armer.LoadMembership(rosters, actives)
			// Reconcile is post-replay, including when replay has no messages.
			rp.armer.Reconcile(rosters, actives, nil)
			carryReading(rp, 2, "v2", 21, true)
			carryFlush(t, rp, 21)
			if len(sink.sent) != 1 || len(w.edges) != 1 {
				t.Fatalf("restart re-fired: %+v", w.edges)
			}
		})
	}
}

func TestReplayPreHandoffResolveStillPublished(t *testing.T) {
	rp, w, _ := publishCarryProc(t, dc.Threshold)
	carryReading(rp, 1, "v1", 1, true)
	carryFlush(t, rp, 1)
	rosters := []runtime.RosterEntry{{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}}
	actives := []runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase.Add(20 * time.Second)}}
	// Startup knows v2 membership BEFORE replay, but must not move the v1 latch yet.
	sr := absScopedProc("acme", "p1@v2", "acme/p1@v2/r1")
	sr.Compiled.Core.Kind = dc.Threshold
	sr.Compiled.Type = "threshold"
	rp.registry.Upsert(sr)
	rp.engine.UpsertRule(sr.Compiled.Core)
	rp.armer.LoadMembership(rosters, actives)
	carryReading(rp, 2, "v1", 10, false)
	carryFlush(t, rp, 10)
	if len(w.edges) != 2 || w.edges[1].Edge != runtime.EdgeResolved {
		t.Fatalf("replay lost old-version clear: %+v", w.edges)
	}
	rp.armer.Reconcile(rosters, actives, nil)
	carryReading(rp, 3, "v2", 21, true)
	carryFlush(t, rp, 21)
	if len(w.edges) != 3 || w.edges[2].Edge != runtime.EdgeRaised {
		t.Fatalf("replay left a cleared alarm latched: %+v", w.edges)
	}
}
