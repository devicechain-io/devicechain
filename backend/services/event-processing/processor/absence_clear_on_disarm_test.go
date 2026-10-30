// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// edgeWriter records the (series, edge) of every derived event written, in order.
type edgeWriter struct{ got []string }

func (w *edgeWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	for _, m := range msgs {
		var de runtime.DerivedEvent
		if err := json.Unmarshal(m.Value, &de); err != nil {
			return err
		}
		w.got = append(w.got, de.Series+":"+de.Edge)
	}
	return nil
}
func (w *edgeWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *edgeWriter) HandleResponse(error) {}

// raisedAbsenceProc builds an armed processor whose device dev1 (profile p1, active p1@v1) has an
// ABSENCE alarm RAISED and already published, with the capture reset — the state just before a
// control-plane departure (disable / delete / re-type) of the device.
func raisedAbsenceProc(t *testing.T) (*ResolvedEventsProcessor, *edgeWriter) {
	t.Helper()
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	w := &edgeWriter{}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)

	eng.Advance(testBase.Add(11 * time.Second)) // past the 10s dead-man deadline: the absence raises
	rp.drainDetections(testBase.Add(11*time.Second), true)
	if !rp.publishPending(context.Background()) {
		t.Fatal("publishing the raise failed")
	}
	if len(w.got) != 1 || w.got[0] != "dev1:raised" {
		t.Fatalf("setup: want the absence raised and published, got %v", w.got)
	}
	w.got = nil
	return rp, w
}

// TestAbsenceClearPublishedWhenDeviceLeavesScope: an absence alarm raised for a device must be CLEARED
// downstream when the device leaves the rule's scope — disabled (roster fact with an empty profile),
// deleted, or re-typed to another profile. The disarm emits the Resolved edge, but the device is by
// then not AbsenceLive, so the publish-time gate (which exists to discard stale RAISES) must let the
// falling edge through, or the alarm stays raised forever.
func TestAbsenceClearPublishedWhenDeviceLeavesScope(t *testing.T) {
	cases := []struct {
		name  string
		apply func(rp *ResolvedEventsProcessor)
	}{
		{"disabled", func(rp *ResolvedEventsProcessor) {
			rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: ""}, true)
		}},
		{"deleted", func(rp *ResolvedEventsProcessor) {
			rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1"}, false)
		}},
		{"retyped", func(rp *ResolvedEventsProcessor) {
			rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p2", ExpectedSince: testBase}, true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rp, w := raisedAbsenceProc(t)
			tc.apply(rp)
			rp.drainDetections(testBase.Add(12*time.Second), true)
			if !rp.publishPending(context.Background()) {
				t.Fatal("publish failed")
			}
			if len(w.got) != 1 || w.got[0] != "dev1:resolved" {
				t.Fatalf("the clear for a departed device was not published, got %v", w.got)
			}
		})
	}
}

// TestAbsenceRaiseAndClearInOneBatchBothDropped: when the device leaves scope BEFORE its raise was ever
// published (both edges still buffered), downstream never saw a raise, so it is owed no clear either:
// the stale raise is dropped and its paired clear goes with it.
func TestAbsenceRaiseAndClearInOneBatchBothDropped(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	w := &edgeWriter{}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)

	eng.Advance(testBase.Add(11 * time.Second))
	rp.drainDetections(testBase.Add(11*time.Second), true) // raise buffered, not yet published
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: ""}, true)
	rp.drainDetections(testBase.Add(12*time.Second), true) // its clear buffered behind it

	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	if len(w.got) != 0 {
		t.Fatalf("a raise that never left the buffer must not publish a raise or a clear, got %v", w.got)
	}
}

// TestAbsenceClearThenRearmInOneBatchKeepsOrder: a device disabled and re-enabled within one batch keeps
// its clear, and a LATER re-raise (live again) is not suppressed or reordered by it.
func TestAbsenceClearThenRearmInOneBatchKeepsOrder(t *testing.T) {
	rp, w := raisedAbsenceProc(t)
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: ""}, true)
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase.Add(13 * time.Second)}, true)
	rp.drainDetections(testBase.Add(12*time.Second), true)
	rp.engine.Advance(testBase.Add(30 * time.Second)) // the re-armed dead-man elapses: a fresh raise
	rp.drainDetections(testBase.Add(30*time.Second), true)

	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	want := []string{"dev1:resolved", "dev1:raised"}
	if len(w.got) != 2 || w.got[0] != want[0] || w.got[1] != want[1] {
		t.Fatalf("want %v, got %v", want, w.got)
	}
}

// twoVersionProc is raisedAbsenceProc over a registry that also holds a p1@v2 absence rule (ruleV2 is
// its rule token; "r1" carries the v1 rule forward under the same stable identity, anything else
// removes it). dev1 has its v1 absence raised and published, the capture reset.
func twoVersionProc(t *testing.T, ruleV2 string) (*ResolvedEventsProcessor, *edgeWriter) {
	t.Helper()
	v1 := runtime.ComposeRuleID("acme", "p1@v1/r1")
	v2 := runtime.ComposeRuleID("acme", "p1@v2/"+ruleV2)
	rp, _ := armedProcessor(t, v1)
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{absScopedProc("acme", "p1@v1", v1), absScopedProc("acme", "p1@v2", v2)})
	eng := detectcore.NewEngine(reg.Cores(), 0)
	rp.engine, rp.registry = eng, reg
	rp.armer = runtime.NewDeadmanArmer(reg, eng)
	w := &edgeWriter{}
	rp.publisher = runtime.NewPublisher(w, reg, (*DetectMetrics)(nil))
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)
	eng.Advance(testBase.Add(11 * time.Second))
	rp.drainDetections(testBase.Add(11*time.Second), true)
	if !rp.publishPending(context.Background()) || len(w.got) != 1 || w.got[0] != "dev1:raised" {
		t.Fatalf("setup: want the v1 absence raised and published, got %v", w.got)
	}
	w.got = nil
	return rp, w
}

// TestSupersessionOfAStillSilentDevicePublishesNoClear: a republish disarms the old version's dead-man
// for a device that STAYS in scope, which emits a Resolved. The alarm contributor id is stable across
// versions, so publishing it would flap a still-silent device's alarm CLEARED then ACTIVE (resetting its
// acknowledgement); the handoff is dropped.
func TestSupersessionOfAStillSilentDevicePublishesNoClear(t *testing.T) {
	rp, w := twoVersionProc(t, "r1")
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase.Add(20 * time.Second)})
	rp.drainDetections(testBase.Add(21*time.Second), true)
	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	if len(w.got) != 0 {
		t.Fatalf("a still-silent device's republish handoff must publish nothing, got %v", w.got)
	}
}

// TestRepublishThatRemovesTheRuleStillClears: when the new version no longer carries the rule, nothing
// continues the alarm, so the clear must reach the feed.
func TestRepublishThatRemovesTheRuleStillClears(t *testing.T) {
	rp, w := twoVersionProc(t, "other")
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase.Add(20 * time.Second)})
	rp.drainDetections(testBase.Add(21*time.Second), true)
	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	if len(w.got) != 1 || w.got[0] != "dev1:resolved" {
		t.Fatalf("want the clear for the removed rule, got %v", w.got)
	}
}

// TestAbsencePairingIsPerKey: two devices disabled in one batch, A's raise still buffered and B's already
// published. A's raise and clear are both dropped; B's clear is published.
func TestAbsencePairingIsPerKey(t *testing.T) {
	rp, w := raisedAbsenceProc(t) // dev1 (B): raised + published
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: testBase.Add(12 * time.Second)}, true)
	rp.engine.Advance(testBase.Add(30 * time.Second))
	rp.drainDetections(testBase.Add(30*time.Second), true) // devA raise buffered, unpublished
	for _, dev := range []string{"devA", "dev1"} {
		rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: dev, ProfileToken: ""}, true)
	}
	rp.drainDetections(testBase.Add(31*time.Second), true)
	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	if len(w.got) != 1 || w.got[0] != "dev1:resolved" {
		t.Fatalf("want only dev1's clear, got %v", w.got)
	}
}
