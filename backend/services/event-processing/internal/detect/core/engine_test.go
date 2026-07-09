// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"testing"
	"time"
)

// base is a fixed epoch so every derived timestamp is deterministic (no wall clock).
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

// step is one input: an event, or an idle watermark advance (adv != nil).
type step struct {
	ev  *Event
	adv *time.Time
}

func evStep(seq uint64, rule, series string, sec int, match bool) step {
	return step{ev: &Event{Seq: seq, Key: SeriesKey{Rule: rule, Series: series}, Time: at(sec), Match: match}}
}

func evValStep(seq uint64, rule, series string, sec int, val float64) step {
	return step{ev: &Event{Seq: seq, Key: SeriesKey{Rule: rule, Series: series}, Time: at(sec), Value: val, Match: true}}
}

func advStep(sec int) step {
	t := at(sec)
	return step{adv: &t}
}

// scenario exercises all three Slice-0 rule shapes, including the two hard cases:
//   - Absence RE-ARMING after it fires (dev1 fires at 15, then again at 31).
//   - A Duration run that is CANCELLED before it matures (must NOT fire) followed by one
//     that does — proving cancel actually invalidates the pending timer.
func scenario() ([]Rule, []step) {
	rules := []Rule{
		{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second},
		{ID: "rDur", Kind: Duration, Hold: 8 * time.Second},
		{ID: "rThr", Kind: Threshold},
		{ID: "rRep", Kind: Repeating, Window: 10 * time.Second, Count: 3},
		{ID: "rAgg", Kind: Aggregate, Window: 10 * time.Second, Agg: AggAvg, Op: GT, Thresh: 100},
	}
	steps := []step{
		evStep(1, "rAbs", "dev1", 0, true),   // arm dead-man -> deadline 10
		evStep(2, "rThr", "dev3", 1, false),  // no fire
		evStep(3, "rDur", "dev2", 2, true),   // duration active since 2 -> deadline 10
		evStep(4, "rThr", "dev3", 3, true),   // FIRE threshold @3
		evStep(5, "rAbs", "dev1", 5, true),   // reset dead-man -> deadline 15
		evStep(6, "rDur", "dev2", 6, true),   // still active, no reschedule
		evStep(7, "rDur", "dev2", 9, false),  // CANCEL (held 2..9 = 7s < 8) -> must not fire
		evStep(8, "rDur", "dev2", 11, true),  // duration active since 11 -> deadline 19
		advStep(16),                          // wm=16: FIRE absence dev1 @15
		evStep(9, "rThr", "dev3", 17, true),  // FIRE threshold @17
		advStep(20),                          // wm=20: FIRE duration dev2 @19
		evStep(10, "rAbs", "dev1", 21, true), // re-arm dead-man -> deadline 31
		advStep(35),                          // wm=35: FIRE absence dev1 @31

		// Repeating: >= 3 matching in a sliding 10s window (edge-triggered).
		evStep(11, "rRep", "dev4", 40, true), // count 1 in (30,40]
		evStep(12, "rRep", "dev4", 41, true), // count 2
		evStep(13, "rRep", "dev4", 42, true), // count 3 -> FIRE repeating @42
		evStep(14, "rRep", "dev4", 43, true), // count 4, no rising edge -> no fire
		// Aggregate: tumbling 10s avg > 100.
		evValStep(15, "rAgg", "dev5", 44, 90),  // window [40,50) pane: 90
		evValStep(16, "rAgg", "dev5", 46, 120), // window [40,50) avg (90+120)/2=105 > 100
		evValStep(17, "rAgg", "dev5", 52, 80),  // window [50,60) pane: 80 (advances wm=52 -> closes [40,50) -> FIRE agg @50)
		evValStep(18, "rAgg", "dev5", 54, 90),  // window [50,60) avg (80+90)/2=85, not > 100
		// Repeating re-arm after the count drains out of the window.
		evStep(19, "rRep", "dev4", 60, true), // (50,60]: prior events evicted -> count 1
		evStep(20, "rRep", "dev4", 61, true), // count 2
		evStep(21, "rRep", "dev4", 62, true), // count 3 -> FIRE repeating @62
		advStep(65),                          // wm=65: closes [50,60) avg 85 -> no fire
	}
	return rules, steps
}

func expected() map[Detection]bool {
	return map[Detection]bool{
		{RuleID: "rThr", Series: "dev3", Kind: Threshold, At: at(3)}:  true,
		{RuleID: "rAbs", Series: "dev1", Kind: Absence, At: at(15)}:   true,
		{RuleID: "rThr", Series: "dev3", Kind: Threshold, At: at(17)}: true,
		{RuleID: "rDur", Series: "dev2", Kind: Duration, At: at(19)}:  true,
		{RuleID: "rAbs", Series: "dev1", Kind: Absence, At: at(31)}:   true,
		{RuleID: "rRep", Series: "dev4", Kind: Repeating, At: at(42)}: true,
		{RuleID: "rAgg", Series: "dev5", Kind: Aggregate, At: at(50)}: true,
		{RuleID: "rRep", Series: "dev4", Kind: Repeating, At: at(62)}: true,
	}
}

func applyStep(e *Engine, s step) {
	if s.ev != nil {
		e.ProcessEvent(*s.ev)
	} else {
		e.Advance(*s.adv)
	}
}

// runClean feeds every step to a fresh engine and returns all detections.
func runClean(rules []Rule, steps []step) []Detection {
	e := NewEngine(rules, 0)
	var all []Detection
	for _, s := range steps {
		applyStep(e, s)
		all = append(all, e.Drain()...)
	}
	return all
}

func dedup(ds []Detection) map[Detection]bool {
	set := map[Detection]bool{}
	for _, d := range ds {
		set[d] = true
	}
	return set
}

func TestCleanRunMatchesExpected(t *testing.T) {
	rules, steps := scenario()
	got := dedup(runClean(rules, steps))
	assertSetEqual(t, expected(), got)
}

func TestDeterministic(t *testing.T) {
	rules, steps := scenario()
	a := runClean(rules, steps)
	b := runClean(rules, steps)
	if len(a) != len(b) {
		t.Fatalf("nondeterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestReplayCorrectness is the spine of Slice 0. For EVERY (checkpoint, crash) pair it:
//  1. processes steps 0..crash, snapshotting the state at `checkpoint` (ack-on-checkpoint);
//  2. discards the in-memory engine (kill -9) and restores from the checkpoint snapshot;
//  3. replays every step after the checkpoint.
//
// Steps between checkpoint and crash are processed twice, so detections re-emit at-least-
// once — but because every detection's identity is deterministic, dedup-collapsing the
// union must exactly equal the clean run: NO missed detection, NO false one.
func TestReplayCorrectness(t *testing.T) {
	rules, steps := scenario()
	want := expected()
	n := len(steps)

	for checkpoint := 0; checkpoint < n; checkpoint++ {
		for crash := checkpoint; crash < n; crash++ {
			t.Run(fmt.Sprintf("cp=%d_crash=%d", checkpoint, crash), func(t *testing.T) {
				e := NewEngine(rules, 0)
				var all []Detection
				var snap []byte

				for idx := 0; idx <= crash; idx++ {
					applyStep(e, steps[idx])
					all = append(all, e.Drain()...)
					if idx == checkpoint {
						b, err := e.Snapshot()
						if err != nil {
							t.Fatalf("snapshot: %v", err)
						}
						snap = b
					}
				}

				// kill -9 -> restore from the last committed checkpoint.
				e, err := Restore(rules, 0, snap)
				if err != nil {
					t.Fatalf("restore: %v", err)
				}
				for idx := checkpoint + 1; idx < n; idx++ {
					applyStep(e, steps[idx])
					all = append(all, e.Drain()...)
				}

				assertSetEqual(t, want, dedup(all))
			})
		}
	}
}

func assertSetEqual(t *testing.T, want, got map[Detection]bool) {
	t.Helper()
	for d := range want {
		if !got[d] {
			t.Errorf("MISSED detection: %+v", d)
		}
	}
	for d := range got {
		if !want[d] {
			t.Errorf("FALSE detection: %+v", d)
		}
	}
}

// BenchmarkThroughput sweeps rule/series count — the real scaling axis (ADR-052 note:
// eKuiper scales by rule count, not msg/s). Run: go test -bench . -benchmem
func BenchmarkThroughput(b *testing.B) {
	for _, series := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			rules := []Rule{{ID: "rAbs", Kind: Absence, Timeout: 30 * time.Second}}
			e := NewEngine(rules, 0)
			devs := make([]string, series)
			for i := range devs {
				devs[i] = fmt.Sprintf("dev%d", i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var seq uint64
			for i := 0; i < b.N; i++ {
				seq++
				e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: "rAbs", Series: devs[i%series]}, Time: base.Add(time.Duration(i) * time.Millisecond), Match: true})
				e.Drain()
			}
		})
	}
}
