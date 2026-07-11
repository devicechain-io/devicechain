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
	return step{ev: &Event{Seq: seq, Key: SeriesKey{Rule: rule, Series: series}, Time: at(sec), Value: val, HasValue: true, Match: true}}
}

func evCorrStep(seq uint64, rule, anchor, member string, sec int) step {
	return step{ev: &Event{Seq: seq, Key: SeriesKey{Rule: rule, Series: anchor}, Member: member, Time: at(sec), Match: true}}
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
		// Slice 2b primitives.
		{ID: "rDelta", Kind: DeltaRate, Op: GT, Thresh: 50},
		{ID: "rCount", Kind: CountWindow, Count: 3, Agg: AggSum, Op: GT, Thresh: 10},
		{ID: "rSess", Kind: Session, Gap: 5 * time.Second, Agg: AggCount, Op: GE, Thresh: 3},
		{ID: "rSlide", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggMax, Op: GT, Thresh: 100},
		{ID: "rSlideSum", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggSum, Op: GT, Thresh: 1.0},
		{ID: "rCorr", Kind: Correlation, Window: 10 * time.Second, Count: 3, MemberCap: 100},
		{ID: "rAbs2", Kind: Absence, Timeout: 10 * time.Second},
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

		// DeltaRate: fire when the jump between consecutive samples exceeds 50.
		evValStep(22, "rDelta", "dev6", 70, 100), // prime (no delta yet)
		evValStep(23, "rDelta", "dev6", 72, 160), // +60 -> FIRE delta @72
		evValStep(24, "rDelta", "dev6", 74, 180), // +20 -> no
		evValStep(25, "rDelta", "dev6", 76, 240), // +60 -> FIRE delta @76
		// CountWindow: every 3 matching events, sum > 10.
		evValStep(26, "rCount", "dev7", 80, 4),
		evValStep(27, "rCount", "dev7", 81, 4),
		evValStep(28, "rCount", "dev7", 82, 4), // 3rd -> sum 12 > 10 -> FIRE count @82, reset
		evValStep(29, "rCount", "dev7", 83, 1),
		evValStep(30, "rCount", "dev7", 84, 1),
		evValStep(31, "rCount", "dev7", 85, 1), // 3rd -> sum 3, not > 10 -> no fire
		// Session: gap 5s, >= 3 events in the session; closes on the watermark.
		evValStep(32, "rSess", "dev8", 90, 1),
		evValStep(33, "rSess", "dev8", 91, 1),
		evValStep(34, "rSess", "dev8", 92, 1), // session {90,91,92}; gap deadline 97
		advStep(98),                           // wm=98 -> session closes @97, count 3 >= 3 -> FIRE session @97
		// SlidingAgg: sliding 10s max > 100, edge-triggered.
		evValStep(35, "rSlide", "dev9", 100, 90),  // max 90, no
		evValStep(36, "rSlide", "dev9", 101, 150), // max 150 -> FIRE slide @101 (armed)
		evValStep(37, "rSlide", "dev9", 108, 95),  // max still 150 (101 in window), armed -> no
		evValStep(38, "rSlide", "dev9", 113, 95),  // cutoff 103 evicts 100,101 -> max 95 -> re-arm (no fire)
		evValStep(39, "rSlide", "dev9", 114, 160), // max 160 -> FIRE slide @114
		// Correlation: >= 3 distinct devices reporting in area1 within a sliding 10s window.
		evCorrStep(40, "rCorr", "area1", "devA", 120), // distinct 1
		evCorrStep(41, "rCorr", "area1", "devB", 121), // distinct 2
		evCorrStep(42, "rCorr", "area1", "devC", 122), // distinct 3 -> FIRE correlation @122
		evCorrStep(43, "rCorr", "area1", "devA", 123), // known member, count unchanged -> no
		evCorrStep(44, "rCorr", "area1", "devD", 135), // cutoff 125 evicts A,B,C -> distinct 1 -> no

		// Absence with an OUT-OF-ORDER heartbeat: the late (earlier-time) event must not shrink
		// the dead-man deadline (scheduleForward). A gen-recycle or shrink bug fires early @158.
		evStep(45, "rAbs2", "dev10", 150, true), // arm dead-man -> deadline 160
		evStep(46, "rAbs2", "dev10", 148, true), // LATE heartbeat: forward-only keeps deadline 160
		advStep(159),                            // wm=159: 160 > 159 -> NO fire
		advStep(165),                            // wm=165: FIRE absence dev10 @160
		// SlidingAgg re-arm across a SILENT gap: the window empties by time alone (no low sample),
		// so the next breach must be a fresh crossing, not a swallowed one.
		evValStep(47, "rSlide", "dev9", 200, 170), // cutoff 190 evicts all -> re-arm -> FIRE slide @200
		// SlidingAgg over a fractional SUM with eviction residue: exercises verbatim-sum restore.
		evValStep(48, "rSlideSum", "dev11", 210, 0.4), // sum 0.4, no
		evValStep(49, "rSlideSum", "dev11", 211, 0.4), // sum 0.8, no
		evValStep(50, "rSlideSum", "dev11", 212, 0.4), // sum 1.2 > 1.0 -> FIRE @212 (armed)
		evValStep(51, "rSlideSum", "dev11", 220, 0.4), // cutoff 215 evicts 210-212 -> sum 0.4 -> re-arm
		evValStep(52, "rSlideSum", "dev11", 221, 0.4), // sum 0.8, no
		evValStep(53, "rSlideSum", "dev11", 222, 0.4), // sum 1.2 > 1.0 -> FIRE @222
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
		// Slice 2b primitives.
		{RuleID: "rDelta", Series: "dev6", Kind: DeltaRate, At: at(72)}:       true,
		{RuleID: "rDelta", Series: "dev6", Kind: DeltaRate, At: at(76)}:       true,
		{RuleID: "rCount", Series: "dev7", Kind: CountWindow, At: at(82)}:     true,
		{RuleID: "rSess", Series: "dev8", Kind: Session, At: at(97)}:          true,
		{RuleID: "rSlide", Series: "dev9", Kind: SlidingAgg, At: at(101)}:     true,
		{RuleID: "rSlide", Series: "dev9", Kind: SlidingAgg, At: at(114)}:     true,
		{RuleID: "rCorr", Series: "area1", Kind: Correlation, At: at(122)}:    true,
		{RuleID: "rAbs2", Series: "dev10", Kind: Absence, At: at(160)}:        true,
		{RuleID: "rSlide", Series: "dev9", Kind: SlidingAgg, At: at(200)}:     true,
		{RuleID: "rSlideSum", Series: "dev11", Kind: SlidingAgg, At: at(212)}: true,
		{RuleID: "rSlideSum", Series: "dev11", Kind: SlidingAgg, At: at(222)}: true,
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

// identity strips the informational Value payload (slice 6a), leaving the dedup identity
// (RuleID, Series, Kind, At) — the exact key a downstream at-least-once collapse uses. Value is
// deterministic too (TestDeterministic compares full structs), but it is not part of identity, so
// the replay/expected set comparison must not depend on it.
func identity(d Detection) Detection {
	d.Value, d.HasValue = 0, false
	return d
}

func dedup(ds []Detection) map[Detection]bool {
	set := map[Detection]bool{}
	for _, d := range ds {
		set[identity(d)] = true
	}
	return set
}

// TestDetectionValueCarried proves slice 6a's value carriage: a value-bearing fire stamps the
// scalar it is about (the crossing sample for Threshold, the computed delta for DeltaRate, the
// window aggregate for Aggregate), while a silence-driven fire (Absence, Duration) carries none
// (HasValue=false). Value is what a raiseAlarm REACT action stamps on the alarm, so a wrong or
// missing value here is the slice-5c "re-raise clobbers with 0" blocker regressing.
func TestDetectionValueCarried(t *testing.T) {
	rules := []Rule{
		{ID: "rThr", Kind: Threshold},
		{ID: "rDelta", Kind: DeltaRate, Op: GT, Thresh: 50},
		{ID: "rAgg", Kind: Aggregate, Window: 10 * time.Second, Agg: AggAvg, Op: GT, Thresh: 100},
		{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second},
		{ID: "rDur", Kind: Duration, Hold: 5 * time.Second},
	}
	steps := []step{
		evValStep(1, "rThr", "d", 1, 42.5),  // Threshold: crossing sample 42.5
		evValStep(2, "rDelta", "d", 2, 100), // prime
		evValStep(3, "rDelta", "d", 4, 200), // +100 delta -> value 100
		evValStep(4, "rAgg", "d", 5, 90),    // pane [0,10)
		evValStep(5, "rAgg", "d", 6, 120),   // pane avg (90+120)/2 = 105
		advStep(12),                         // close pane -> Aggregate value 105
		evStep(6, "rAbs", "d", 1, true),     // arm dead-man
		evStep(7, "rDur", "d", 1, true),     // arm duration
		advStep(30),                         // fire Absence + Duration (silence-driven, no value)
	}
	got := map[Detection]Detection{} // identity -> full
	e := NewEngine(rules, 0)
	for _, s := range steps {
		applyStep(e, s)
		for _, d := range e.Drain() {
			got[identity(d)] = d
		}
	}
	check := func(rule string, kind RuleKind, at time.Time, wantHas bool, wantVal float64) {
		t.Helper()
		d, ok := got[Detection{RuleID: rule, Series: "d", Kind: kind, At: at}]
		if !ok {
			t.Fatalf("%s: no detection at %v", rule, at)
		}
		if d.HasValue != wantHas || (wantHas && d.Value != wantVal) {
			t.Errorf("%s: HasValue=%v Value=%v; want HasValue=%v Value=%v", rule, d.HasValue, d.Value, wantHas, wantVal)
		}
	}
	check("rThr", Threshold, at(1), true, 42.5)
	check("rDelta", DeltaRate, at(4), true, 100)
	check("rAgg", Aggregate, at(10), true, 105)
	check("rAbs", Absence, at(11), false, 0) // deadline: armed at 1 + 10s timeout
	check("rDur", Duration, at(6), false, 0) // deadline: held from 1 + 5s hold
}

// TestReplayRestoresValue guards the value payload across a snapshot/restore, coverage the
// identity-projected dedup deliberately drops. A DeltaRate's emitted value is the delta between the
// current sample and the PRIOR sample held in snapshotted state — so if restore lost or corrupted
// that prior sample, the delta (and thus the alarm's stamped value) would be wrong while the
// detection identity stayed correct. Feed a priming sample, snapshot, kill, restore, then feed the
// next sample and assert the delta value is exact.
func TestReplayRestoresValue(t *testing.T) {
	rules := []Rule{{ID: "rd", Kind: DeltaRate, Op: GT, Thresh: 50}}
	e := NewEngine(rules, 0)
	e.ProcessEvent(Event{Seq: 1, Key: SeriesKey{Rule: "rd", Series: "d"}, Time: at(1), Value: 100, HasValue: true, Match: true})
	if len(e.Drain()) != 0 {
		t.Fatal("priming sample must not fire (needs two samples for a delta)")
	}
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// kill -9 -> restore from the checkpoint that holds the prior sample (100).
	e2, err := Restore(rules, 0, snap)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	e2.ProcessEvent(Event{Seq: 2, Key: SeriesKey{Rule: "rd", Series: "d"}, Time: at(3), Value: 160, HasValue: true, Match: true})
	d := e2.Drain()
	if len(d) != 1 || !d[0].HasValue || d[0].Value != 60 {
		t.Fatalf("delta value after restore must be 160-100=60 (proving the prior sample restored); got %+v", d)
	}
}

// TestLiveKeyCounts proves the per-rule live-key ENTRY accounting the ADR-023 state budget measures
// (slice 6c): every live map/wheel/pane entry is counted (a memory proxy), a timer-bearing key
// counts in BOTH its state map and the wheel, and — critically — a heartbeat-armed ABSENCE rule
// (whose timer lives ONLY in the wheel) is counted, not silently zero. Removing a rule zeroes it.
func TestLiveKeyCounts(t *testing.T) {
	rules := []Rule{
		{ID: "acme/p@1/dur", Kind: Duration, Hold: 10 * time.Second},
		{ID: "acme/p@1/delta", Kind: DeltaRate, Op: GT, Thresh: 1},
		{ID: "acme/p@1/agg", Kind: Aggregate, Window: 10 * time.Second, Agg: AggAvg, Op: GT, Thresh: 0},
		{ID: "acme/p@1/abs", Kind: Absence, Timeout: 10 * time.Second},
		{ID: "acme/p@1/corr", Kind: Correlation, Window: 10 * time.Second, Count: 5, MemberCap: 100},
	}
	e := NewEngine(rules, 0)
	// dur: two devices holding -> 2 active + 2 wheel timers = 4 entries.
	// delta: one device primed -> 1 lastVal, no timer -> 1. agg: one open pane -> 1.
	// abs: one device reporting arms a wheel-ONLY dead-man timer -> 1 (the regression guard).
	// corr: one anchor with 3 distinct members -> 1 anchor + 3 members = 4 (not anchor-only 1).
	e.ProcessEvent(Event{Seq: 1, Key: SeriesKey{Rule: "acme/p@1/dur", Series: "d1"}, Time: at(1), Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: SeriesKey{Rule: "acme/p@1/dur", Series: "d2"}, Time: at(1), Match: true})
	e.ProcessEvent(Event{Seq: 3, Key: SeriesKey{Rule: "acme/p@1/delta", Series: "d1"}, Time: at(1), Value: 5, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 4, Key: SeriesKey{Rule: "acme/p@1/agg", Series: "d1"}, Time: at(1), Value: 5, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 5, Key: SeriesKey{Rule: "acme/p@1/abs", Series: "d1"}, Time: at(1), Match: true})
	e.ProcessEvent(Event{Seq: 6, Key: SeriesKey{Rule: "acme/p@1/corr", Series: "area1"}, Member: "mA", Time: at(1), Match: true})
	e.ProcessEvent(Event{Seq: 7, Key: SeriesKey{Rule: "acme/p@1/corr", Series: "area1"}, Member: "mB", Time: at(1), Match: true})
	e.ProcessEvent(Event{Seq: 8, Key: SeriesKey{Rule: "acme/p@1/corr", Series: "area1"}, Member: "mC", Time: at(1), Match: true})
	e.Drain()

	counts := e.LiveKeyCounts()
	if counts["acme/p@1/dur"] != 4 || counts["acme/p@1/delta"] != 1 || counts["acme/p@1/agg"] != 1 {
		t.Fatalf("live-key counts wrong: %+v", counts)
	}
	if counts["acme/p@1/abs"] != 1 {
		t.Fatalf("a heartbeat-armed absence timer (wheel-only) must be counted, not 0; got %d", counts["acme/p@1/abs"])
	}
	if counts["acme/p@1/corr"] != 4 {
		t.Fatalf("correlation must count anchor + members (1+3=4), not anchor-only; got %d", counts["acme/p@1/corr"])
	}

	// Removing a rule GCs its keyed state (maps + wheel), so it no longer contributes.
	e.RemoveRule("acme/p@1/dur")
	if c := e.LiveKeyCounts()["acme/p@1/dur"]; c != 0 {
		t.Fatalf("removed rule must contribute 0 live keys; got %d", c)
	}
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

// BenchmarkSlidingAgg exercises the monotonic-deque sliding-window path (the most
// state-heavy Slice-2b primitive) across series count, so a regression in the deque or
// eviction cost surfaces here rather than in production.
func BenchmarkSlidingAgg(b *testing.B) {
	for _, series := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			rules := []Rule{{ID: "rSlide", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMax, Op: GT, Thresh: 1e9}}
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
				e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: "rSlide", Series: devs[i%series]}, Time: base.Add(time.Duration(i) * time.Millisecond), Value: float64(i % 97), Match: true})
				e.Drain()
			}
		})
	}
}
