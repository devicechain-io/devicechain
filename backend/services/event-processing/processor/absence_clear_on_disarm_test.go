// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	rp.drainDetections(testBase.Add(11 * time.Second))
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
			rp.drainDetections(testBase.Add(12 * time.Second))
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
	rp.drainDetections(testBase.Add(11 * time.Second)) // raise buffered, not yet published
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: ""}, true)
	rp.drainDetections(testBase.Add(12 * time.Second)) // its clear buffered behind it

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
	rp.drainDetections(testBase.Add(12 * time.Second))
	rp.engine.Advance(testBase.Add(30 * time.Second)) // the re-armed dead-man elapses: a fresh raise
	rp.drainDetections(testBase.Add(30 * time.Second))

	if !rp.publishPending(context.Background()) {
		t.Fatal("publish failed")
	}
	want := []string{"dev1:resolved", "dev1:raised"}
	if len(w.got) != 2 || w.got[0] != want[0] || w.got[1] != want[1] {
		t.Fatalf("want %v, got %v", want, w.got)
	}
}
