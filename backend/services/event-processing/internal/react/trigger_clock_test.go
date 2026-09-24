// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
)

// defaultGate is the source gate at the configuration defaults (100/s, burst 200).
func defaultGate() *core.TenantRateLimiter {
	return core.NewTenantRateLimiter(core.StaticCeiling(100, 200))
}

// triggered is a distinct event (its own series, so its own idempotency token) triggered at at.
func triggered(i int, at time.Time) runtime.DerivedEvent {
	ev := evt()
	ev.Series = fmt.Sprintf("device-%05d", i)
	ev.TriggeredAt = at
	return ev
}

// A compliant tenant's backlog drained at once is metered on its trigger times and loses nothing.
// Its device time is an hour in the future and changes nothing: the gate never reads it.
func TestBacklogDrainOfACompliantTenantIsNotShed(t *testing.T) {
	const n = 1000
	sink := &fakeConnectorSink{}
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true},
		nil, nil, sink, defaultGate(), newFakeMetrics())
	now := time.Now()
	future := now.Add(time.Hour)
	for i := 0; i < n; i++ {
		ev := triggered(i, now.Add(-time.Duration(n-i)*100*time.Millisecond))
		ev.OccurredTime = future
		res := d.Dispatch(context.Background(), ev)
		if res.Outcome != Done || len(res.Shed) != 0 {
			t.Fatalf("event %d: outcome %v, shed %+v; a compliant backlog must not be shed", i, res.Outcome, res.Shed)
		}
	}
	if len(sink.got) != n {
		t.Fatalf("dispatched %d of %d", len(sink.got), n)
	}
}

// Metering on trigger time does not make a flood compliant: 1000 actions triggered within one
// second get the burst plus one second of rate, and every other one is shed and reported with its
// own token.
func TestAFloodInTriggerTimeIsStillShed(t *testing.T) {
	const n = 1000
	sink := &fakeConnectorSink{}
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true},
		nil, nil, sink, defaultGate(), newFakeMetrics())
	start := time.Now().Add(-time.Second)
	tokens := map[string]bool{}
	shed := 0
	for i := 0; i < n; i++ {
		res := d.Dispatch(context.Background(), triggered(i, start.Add(time.Duration(i)*time.Millisecond)))
		for _, s := range res.Shed {
			if s.Kind != "httpCall" || s.Token == "" || tokens[s.Token] {
				t.Fatalf("shed %+v: want an httpCall with a token of its own", s)
			}
			tokens[s.Token] = true
			shed++
		}
	}
	if admitted := len(sink.got); admitted < 298 || admitted > 302 {
		t.Fatalf("admitted %d, want 300±2 (burst 200 + 100/s over 1s)", admitted)
	}
	if shed+len(sink.got) != n {
		t.Fatalf("admitted %d + shed %d != %d: a shed went unreported", len(sink.got), shed, n)
	}
}

// The trigger time is metering, not identity: two dispatches of one detection stamped at
// different times carry the same idempotency token, so the sink still dedups a replay.
func TestTriggeredAtIsNotInTheIdempotencyToken(t *testing.T) {
	a := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y"}}
	ev1, ev2 := evt(), evt()
	ev1.TriggeredAt = time.Now()
	ev2.TriggeredAt = ev1.TriggeredAt.Add(time.Hour)
	if idempotencyToken(ev1, a) != idempotencyToken(ev2, a) {
		t.Fatal("the idempotency token changed with the trigger time")
	}
	if ev1.DedupID() != ev2.DedupID() {
		t.Fatal("the derived event's dedup id changed with the trigger time")
	}
}

// Two event-sources pods whose clocks are ten minutes apart stamp one tenant's interleaved
// events. The older clock's times arrive behind the bucket's mark, so they are charged there and
// never rewind it: the tenant gets at most the burst plus the rate over the span it was charged
// across, however the two timelines interleave.
func TestSkewedProcessedTimelinesThroughREACT(t *testing.T) {
	const n, rate, burst = 2000, 100.0, 200
	sink := &fakeConnectorSink{}
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true},
		nil, nil, sink, core.NewTenantRateLimiter(core.StaticCeiling(rate, burst)), newFakeMetrics())
	// 200 a second for ten seconds, ending now, alternating between the two pods.
	span := 10 * time.Second
	start := time.Now().Add(-span)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * span / n)
		if i%2 == 1 {
			at = at.Add(-10 * time.Minute) // the pod whose clock is behind
		}
		d.Dispatch(context.Background(), triggered(i, at))
	}
	if limit := burst + int(rate*span.Seconds()) + 1; len(sink.got) > limit {
		t.Fatalf("admitted %d, over burst + rate·span = %d: the skewed clock minted tokens", len(sink.got), limit)
	}
}
