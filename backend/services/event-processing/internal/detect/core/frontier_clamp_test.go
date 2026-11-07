// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// The frontier is shared by every tenant and only moves forward, so a device whose clock runs
// ahead must not be able to carry it past the other tenants' deadlines. The resolver admits event
// times up to minutes ahead of the server clock, a figure far above the lateness, so without the
// clamp one such device fires every tenant's absence timers early and makes their windowed
// samples late.
//
// This is the NEGATIVE CONTROL: tenant A's device is 290 s fast; tenant B's rules and devices are
// on time. It fails on an engine that folds the raw event time into the frontier.

type apply func(e *Engine, seq uint64, occurred, stored time.Time, evs []Event)

var clampPaths = map[string]apply{
	"message": func(e *Engine, seq uint64, occurred, stored time.Time, evs []Event) {
		e.ProcessReceived(seq, occurred, Receipt{Received: stored, Stored: stored}, evs)
	},
	"envelope member": func(e *Engine, seq uint64, occurred, stored time.Time, evs []Event) {
		e.ProcessReceivedMember(seq, occurred, Receipt{Received: stored, Stored: stored}, evs)
	},
}

func clampRules() []Rule {
	return []Rule{
		{ID: "b-absence", Kind: Absence, Timeout: 60 * time.Second},
		{ID: "b-max", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggMax, Op: GT, Thresh: 100},
	}
}

func TestFastTenantClockDoesNotMoveAnotherTenantsTimersOrWindows(t *testing.T) {
	const lateness = 5 * time.Second
	for name, feed := range clampPaths {
		t.Run(name, func(t *testing.T) {
			e := NewEngine(clampRules(), lateness)
			bAbs := SeriesKey{Rule: "b-absence", Series: "tenantB/dev"}
			bMax := SeriesKey{Rule: "b-max", Series: "tenantB/dev"}

			// Tenant B's device reports on time: its absence timer is armed for 160.
			feed(e, 1, at(100), at(100), []Event{{Seq: 1, Key: bAbs, Time: at(100), Match: true}})

			// Tenant A's device stamps events 290 s ahead of when the broker stores them.
			feed(e, 2, at(391), at(101), nil)

			if got, want := e.Watermark(), at(101); got.After(want) {
				t.Fatalf("frontier = %v, past the broker append time %v: a device clock moved the shared frontier", got, want)
			}
			if d := e.Drain(); len(d) != 0 {
				t.Fatalf("tenant B's absence timer (deadline 160) fired at append time 101: %+v", d)
			}

			// Tenant B's windowed sample, on time, must still be admitted.
			feed(e, 3, at(102), at(102), []Event{{Seq: 3, Key: bMax, Time: at(102), Value: 500, HasValue: true, Match: true}})
			if n := e.DrainLateSamples(); n != 0 {
				t.Errorf("tenant B's on-time sample was refused as late %d time(s): the frontier ran ahead of real time", n)
			}
			d := e.Drain()
			if len(d) != 1 || d[0].RuleID != "b-max" {
				t.Fatalf("tenant B's window breach was not detected: %+v", d)
			}

			// The clamp bounds the frontier only: once real time reaches the deadline the timer fires.
			feed(e, 4, at(165), at(165), nil)
			d = e.Drain()
			if len(d) != 1 || d[0].RuleID != "b-absence" {
				t.Errorf("tenant B's absence did not fire once append time passed its deadline: %+v", d)
			}
		})
	}
}

// A device whose clock is merely within the lateness of the broker's is untouched: the clamp is
// a bound, not a rewrite, and the event keeps its own instant.
func TestClampLeavesAnOnTimeEventAlone(t *testing.T) {
	e := NewEngine(clampRules(), 5*time.Second)
	// occurred 104 stored 100: 4 s fast, inside the 5 s budget.
	e.ProcessReceived(1, at(104), Receipt{Stored: at(100)}, nil)
	if got, want := e.Watermark(), at(99); !got.Equal(want) {
		t.Fatalf("frontier = %v, want %v (occurred - lateness)", got, want)
	}
	// 12 s fast: clamped to append time.
	e.ProcessReceived(2, at(112), Receipt{Stored: at(100)}, nil)
	if got, want := e.Watermark(), at(100); !got.Equal(want) {
		t.Fatalf("frontier = %v, want %v (append time)", got, want)
	}
}

// An unknown append time cannot clamp (the bare entry points carry none). It must not panic or
// move the frontier backward.
func TestClampWithoutAppendTimeIsUnclamped(t *testing.T) {
	e := NewEngine(clampRules(), 5*time.Second)
	e.ProcessResolved(1, at(391), nil)
	if got, want := e.Watermark(), at(386); !got.Equal(want) {
		t.Fatalf("frontier = %v, want %v", got, want)
	}
}

// The frontier is versioned in the snapshot so a rolling upgrade can tell which kind it restored,
// in the single engine and through the sharded split and merge.
func TestSnapshotCarriesTheFrontierVersion(t *testing.T) {
	rules := clampRules()
	e := NewEngine(rules, 5*time.Second)
	e.ProcessReceived(1, at(100), Receipt{Stored: at(100)}, nil)
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(rules, 5*time.Second, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.RestoredFrontier() != FrontierVersion {
		t.Errorf("restored frontier = %d, want %d", r.RestoredFrontier(), FrontierVersion)
	}

	s := NewSharded(3, rules, 5*time.Second)
	s.ProcessReceived(1, at(100), Receipt{Stored: at(100)}, nil)
	sb, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rs, err := RestoreSharded(2, rules, 5*time.Second, sb)
	if err != nil {
		t.Fatal(err)
	}
	if rs.RestoredFrontier() != FrontierVersion {
		t.Errorf("sharded restored frontier = %d, want %d", rs.RestoredFrontier(), FrontierVersion)
	}

	// A snapshot written before the field existed restores as version 0 and still works.
	legacy := []byte(`{"watermark":"2026-01-01T00:00:00Z","lastSeq":7}`)
	l, err := Restore(rules, 5*time.Second, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if l.RestoredFrontier() != 0 || l.LastSeq() != 7 {
		t.Errorf("legacy restore = (frontier %d, seq %d), want (0, 7)", l.RestoredFrontier(), l.LastSeq())
	}
}

// A frontier restored from a pre-clamp snapshot may sit ahead of real time. It is derived under the
// current version again only once a clamped message has carried it forward, and a message that
// leaves it where it is proves nothing about it.
func TestRestoredLegacyFrontierMovesToTheCurrentVersionOnceClampedPastIt(t *testing.T) {
	rules := clampRules()
	// A legacy snapshot whose frontier a fast clock had carried to 386.
	legacy := []byte(`{"watermark":"` + at(386).UTC().Format(time.RFC3339Nano) + `","lastSeq":1}`)
	e, err := Restore(rules, 5*time.Second, legacy)
	if err != nil {
		t.Fatal(err)
	}
	// Stored at 120: the clamped frontier is 120, behind the restored 386, so nothing has moved.
	e.ProcessReceived(2, at(125), Receipt{Stored: at(120)}, nil)
	if e.RestoredFrontier() != 0 {
		t.Fatalf("frontier version = %d after a message that did not move the frontier, want 0", e.RestoredFrontier())
	}
	// Stored at 400: the frontier moves to 395 on the strength of an append time.
	e.ProcessReceived(3, at(400), Receipt{Stored: at(400)}, nil)
	if e.RestoredFrontier() != FrontierVersion {
		t.Fatalf("frontier version = %d after a clamped message carried it forward, want %d", e.RestoredFrontier(), FrontierVersion)
	}
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(rules, 5*time.Second, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.RestoredFrontier() != FrontierVersion {
		t.Errorf("the snapshot did not record the version after the move: %d", r.RestoredFrontier())
	}
}
