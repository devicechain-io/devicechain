// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// Descope resolves a raised alarm for the descoped series: when a group-scoped rule's device
// leaves the group (ADR-062 S4), its raised alarm must resolve (emit EdgeResolved) so the
// downstream alarm object is not left latched on a series the rule no longer covers.
func TestDescopeResolvesRaisedAlarm(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold, Op: GT, Thresh: 30}}, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 40, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("a matching sample must raise: %+v", d)
	}
	changed := e.Descope("r", "d", at(5))
	got := e.Drain()
	if !changed {
		t.Fatal("descoping a raised series must report a change")
	}
	if len(got) != 1 || got[0].Edge != EdgeResolved || !got[0].At.Equal(at(5)) {
		t.Fatalf("descope must emit EdgeResolved at the descope time; got %+v", got)
	}
}

// Descope drops a mid-hold Duration timer so it cannot fire spuriously for a departed series —
// the core reason the engine must handle descope (a timer fires off the watermark, with no
// event to re-check scope).
func TestDescopeDropsPendingDurationTimer(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Duration, Op: GT, Thresh: 30, Hold: 10 * time.Second}}, 0)
	// A matching sample opens the hold and arms a timer at t=0+10s. No detection yet.
	if d := feedEvent(e, 1, "r", "d", 0, 40, true); len(d) != 0 {
		t.Fatalf("opening a hold must not fire yet: %+v", d)
	}
	// The device leaves scope at t=1, before the hold matures.
	e.Descope("r", "d", at(1))
	e.Drain()
	// Advancing well past the original hold deadline must NOT fire — the timer was dropped.
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("a descoped hold's timer must not fire; got %+v", got)
	}
}

// A control proving the timer WOULD have fired without the descope (so the previous test is
// meaningful, not vacuously green).
func TestDurationTimerFiresWithoutDescope(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Duration, Op: GT, Thresh: 30, Hold: 10 * time.Second}}, 0)
	feedEvent(e, 1, "r", "d", 0, 40, true)
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 1 || got[0].Edge != EdgeRaised {
		t.Fatalf("without a descope the matured hold must raise; got %+v", got)
	}
}

// Descoping a series with no state and no latch is a no-op (the overwhelmingly common case —
// most events are out of scope for any given scoped rule).
func TestDescopeNoStateIsNoOp(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold, Op: GT, Thresh: 30}}, 0)
	if e.Descope("r", "d", at(5)) {
		t.Fatal("descoping a series with no state must report no change")
	}
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("descoping a stateless series must emit nothing; got %+v", got)
	}
}

// Descoping an unknown rule id is a safe no-op (the rule was removed between fan-out and here).
func TestDescopeUnknownRuleIsNoOp(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold, Op: GT, Thresh: 30}}, 0)
	if e.Descope("ghost", "d", at(5)) {
		t.Fatal("descoping an unknown rule must report no change")
	}
}

// A STALE descope — one whose time precedes the rising edge — must NOT resolve the alarm
// (review D3/F2 falling-edge discipline): an out-of-order older event showing out-of-scope is
// not evidence the series left scope now, when the latest reading still supports the alarm.
func TestDescopeStaleDoesNotResolve(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold, Op: GT, Thresh: 30}}, 0)
	if d := feedEvent(e, 1, "r", "d", 10, 40, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("raise at t=10: %+v", d)
	}
	// A descope stamped BEFORE the rising edge must not clear the latch.
	e.Descope("r", "d", at(5))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("a stale descope must emit no resolve; got %+v", got)
	}
}

// Descope drops one exact SeriesKey only — other series of the same rule keep running.
func TestDescopeIsPerSeries(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Duration, Op: GT, Thresh: 30, Hold: 10 * time.Second}}, 0)
	feedEvent(e, 1, "r", "d1", 0, 40, true) // d1 opens a hold
	feedEvent(e, 2, "r", "d2", 0, 40, true) // d2 opens a hold
	e.Descope("r", "d1", at(1))             // only d1 leaves scope
	e.Drain()
	e.Advance(at(100))
	got := e.Drain()
	if len(got) != 1 || got[0].Series != "d2" || got[0].Edge != EdgeRaised {
		t.Fatalf("only d2's hold should mature; got %+v", got)
	}
}
