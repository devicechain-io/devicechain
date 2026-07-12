// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// drainEdges runs one event and returns its detections — a terse helper for the edge tests.
func feedEvent(e *Engine, seq uint64, rule, series string, sec int, val float64, match bool) []Detection {
	e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: rule, Series: series}, Time: at(sec), Value: val, HasValue: true, Match: match})
	return e.Drain()
}

// TestSlidingAggNoSteadyStateFlap is the regression for review D1: a device reporting on a regular
// cadence that divides the window kept the trailing aggregate permanently satisfied, yet the old
// pre-insert resolve read a phantom dip at every sample and emitted Resolved+Raised forever. The
// post-insert-only evaluation must emit exactly ONE Raised for the whole sustained breach.
func TestSlidingAggNoSteadyStateFlap(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 100}}
	e := NewEngine(rules, 0)
	var all []Detection
	for i := 0; i <= 20; i++ { // value 60 every 5s: trailing 10s sum is 120 from t=5 on, never dips
		all = append(all, feedEvent(e, uint64(i+1), "r", "d", i*5, 60, true)...)
	}
	raises, resolves := 0, 0
	for _, d := range all {
		switch d.Edge {
		case EdgeRaised:
			raises++
		case EdgeResolved:
			resolves++
		}
	}
	if raises != 1 || resolves != 0 {
		t.Fatalf("a sustained breach must be one Raised and no Resolved; got %d raised, %d resolved: %+v", raises, resolves, all)
	}
}

// TestSlidingAggResolvesOnGenuineDip proves the falling edge still fires when the aggregate really
// drops below the threshold via a low sample (not a boundary artifact).
func TestSlidingAggResolvesOnGenuineDip(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggMax, Op: GT, Thresh: 100}}
	e := NewEngine(rules, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 150, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("max 150 must raise: %+v", d)
	}
	// cutoff at 11-10=1 evicts the 150@0 sample; the window is {11:40}, max 40 < 100 -> resolve.
	if d := feedEvent(e, 2, "r", "d", 11, 40, true); len(d) != 1 || d[0].Edge != EdgeResolved {
		t.Fatalf("aggregate falling below threshold must resolve: %+v", d)
	}
}

// TestThresholdStaleResolveIgnored is the regression for review D3/F2: under bounded lateness a
// non-matching reading OLDER than the rising edge must NOT resolve the alarm — the latest reading
// still supports it. A stale non-match is ignored (no Resolved, latch stays), so a following match
// does not spuriously re-raise.
func TestThresholdStaleResolveIgnored(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	e := NewEngine(rules, 30*time.Second)
	if d := feedEvent(e, 1, "r", "d", 10, 150, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("match@10 must raise: %+v", d)
	}
	// A delayed non-match stamped at t=8 (before the raise@10) arrives: stale, must be ignored.
	if d := feedEvent(e, 2, "r", "d", 8, 90, false); len(d) != 0 {
		t.Fatalf("a stale out-of-order non-match must not resolve a newer raise: %+v", d)
	}
	// A genuine later non-match resolves it.
	if d := feedEvent(e, 3, "r", "d", 12, 90, false); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(12)) {
		t.Fatalf("a later non-match must resolve: %+v", d)
	}
}

// TestEmitSampleNeutralizesNonFinite is the regression for review F5/H2: a non-finite sample value
// must never reach the detection (it would fail the downstream JSON marshal, a terminal drop that
// wedges the latch). The Raised still fires; it just carries no value.
func TestEmitSampleNeutralizesNonFinite(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		e := NewEngine(rules, 0)
		d := feedEvent(e, 1, "r", "d", 0, v, true)
		if len(d) != 1 || d[0].Edge != EdgeRaised {
			t.Fatalf("a matching non-finite sample must still raise: %+v", d)
		}
		if d[0].HasValue {
			t.Fatalf("a non-finite value must be neutralized to no-value; got HasValue=true value=%v", d[0].Value)
		}
	}
}

// TestClearRaisedUnsuppressesLaterRaise is the regression for review H2/F5: when the runtime
// terminally drops a Raised (a stale-roster absence etc.), it clears the engine latch via
// ClearRaised so a later legitimate fire is not suppressed by a raise nothing downstream saw.
func TestClearRaisedUnsuppressesLaterRaise(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	e := NewEngine(rules, 0)
	key := SeriesKey{Rule: "r", Series: "d"}
	if d := feedEvent(e, 1, "r", "d", 0, 150, true); len(d) != 1 {
		t.Fatalf("first match must raise: %+v", d)
	}
	// The runtime terminally dropped that Raised and clears the latch.
	e.ClearRaised(key)
	// A later match must raise again (not be latch-suppressed).
	if d := feedEvent(e, 2, "r", "d", 5, 150, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("after ClearRaised a later match must raise again, not be suppressed: %+v", d)
	}
}

// edgeCounts tallies raised/resolved detections — a terse helper for the falling-edge tests.
func edgeCounts(ds []Detection) (raises, resolves int) {
	for _, d := range ds {
		switch d.Edge {
		case EdgeRaised:
			raises++
		case EdgeResolved:
			resolves++
		}
	}
	return
}

// TestRepeatingFallingEdgeOnNonMatch is the regression for review D2/D5 (Repeating): a rule with a
// filtering `when` leaf must observe its falling edge while the device keeps reporting NON-matching
// gate-metric values — the burst ages out of the window and the count drops below N — rather than
// staying raised until some future matching event.
func TestRepeatingFallingEdgeOnNonMatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 2}}
	e := NewEngine(rules, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 1, true); len(d) != 0 {
		t.Fatalf("first match must not yet reach count 2: %+v", d)
	}
	if d := feedEvent(e, 2, "r", "d", 1, 1, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("second match in window must raise: %+v", d)
	}
	// The device keeps reporting, but NON-matching (the filtering leaf is false). At t=12 the cutoff
	// is 2, so both matches (@0,@1) age out: count 0 < 2 -> falling edge, even with no new match.
	if d := feedEvent(e, 3, "r", "d", 12, 0, false); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(12)) {
		t.Fatalf("a non-matching event that ages the burst out must resolve: %+v", d)
	}
}

// TestSlidingAggFallingEdgeOnNonMatch is the regression for review D2/D5 (SlidingAgg): a filtering
// rule resolves while the device reports non-matching samples — the qualifying samples age out and
// the aggregate stops satisfying — rather than staying raised until the next match.
func TestSlidingAggFallingEdgeOnNonMatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 100}}
	e := NewEngine(rules, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 150, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("sum 150 > 100 must raise: %+v", d)
	}
	// A non-matching sample at t=12 folds no value but advances eviction (cutoff 2), aging out the
	// 150@0 sample: the window empties, the aggregate no longer satisfies -> resolve.
	if d := feedEvent(e, 2, "r", "d", 12, 999, false); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(12)) {
		t.Fatalf("a non-matching event that ages the window out must resolve: %+v", d)
	}
}

// TestCorrelationFallingEdgeOnNonMatch is the regression for review D2/D5 (Correlation): a rule with
// a filtering member gate resolves while the anchor keeps reporting non-qualifying members — the
// cohort ages out and the distinct count drops below N.
func TestCorrelationFallingEdgeOnNonMatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Correlation, Window: 10 * time.Second, Count: 2, MemberCap: 10}}
	e := NewEngine(rules, 0)
	feedCorr := func(seq uint64, member string, sec int, match bool) []Detection {
		e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: "r", Series: "area"}, Member: member, Time: at(sec), Match: match})
		return e.Drain()
	}
	if d := feedCorr(1, "m0", 0, true); len(d) != 0 {
		t.Fatalf("first distinct member must not reach count 2: %+v", d)
	}
	if d := feedCorr(2, "m1", 1, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("second distinct member in window must raise: %+v", d)
	}
	// A non-qualifying member sighting at t=12 (cutoff 2) ages out m0 and m1: distinct count 0 < 2.
	if d := feedCorr(3, "m2", 12, false); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(12)) {
		t.Fatalf("a non-matching member that ages the cohort out must resolve: %+v", d)
	}
}

// TestMatchEveryUnaffectedByD2D5 pins the invariant that a match-every rule (the common case) is
// byte-identical to the pre-D2/D5 behavior: with no non-matching event, exactly one Raised, no
// Resolved, across the three sliding kinds.
func TestMatchEveryUnaffectedByD2D5(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
	}{
		{"repeating", Rule{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 2}},
		{"slidingAgg", Rule{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine([]Rule{tc.rule}, 0)
			var all []Detection
			for i := 0; i < 5; i++ { // steady matching cadence, breach sustained
				all = append(all, feedEvent(e, uint64(i+1), "r", "d", i, 100, true)...)
			}
			raises, resolves := edgeCounts(all)
			if raises != 1 || resolves != 0 {
				t.Fatalf("%s: match-every sustained breach must be 1 raised / 0 resolved; got %d/%d: %+v", tc.name, raises, resolves, all)
			}
		})
	}
}

// TestMatchGatedFallingEdgeReplayCorrect is the replay spine for the D2/D5 falling edges: it sweeps
// every (checkpoint, crash) pair over a filtering-`when` scenario whose eviction-driven falling edges
// arrive on NON-matching events, and proves the dedup-collapsed union of the crashed+replayed run
// exactly equals the clean run — no missed and no spurious Resolved. The raised latch, sliding buffer,
// and sliding-window state are all snapshotted, so the new resolve edges survive checkpoint/crash.
func TestMatchGatedFallingEdgeReplayCorrect(t *testing.T) {
	rules := []Rule{
		{ID: "rRep", Kind: Repeating, Window: 10 * time.Second, Count: 2},
		{ID: "rSlide", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 100},
	}
	steps := []step{
		evValStep(1, "rRep", "d", 0, 1),     // match
		evValStep(2, "rRep", "d", 1, 1),     // match -> Repeating raise
		evValStep(3, "rSlide", "d", 1, 150), // match -> SlidingAgg raise (sum 150 > 100)
		evStep(4, "rRep", "d", 12, false),   // non-match ages the burst out -> Repeating resolve
		evStep(5, "rSlide", "d", 13, false), // non-match ages the window out -> SlidingAgg resolve
		evValStep(6, "rRep", "d", 20, 1),    // match again (fresh window)
		evValStep(7, "rRep", "d", 21, 1),    // match -> Repeating raise again
	}
	want := dedup(runClean(rules, steps))
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

// TestRemoveExpectedClearsRaisedLatch is the regression for review H1/D6: a device leaving an
// absence rule's scope while its absence is raised must clear the latch (resolving it), so a
// re-created/reused token under the same rule id can raise a fresh dead-man rather than inheriting
// the dead incarnation's suppression.
func TestRemoveExpectedClearsRaisedLatch(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(0))
	e.Advance(at(11)) // fires the dead-man -> Raised@10
	if d := e.Drain(); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("dead-man must raise: %+v", d)
	}
	// The device leaves scope (deleted / re-typed). RemoveExpected resolves the raised absence.
	e.RemoveExpected(key)
	if d := e.Drain(); len(d) != 1 || d[0].Edge != EdgeResolved {
		t.Fatalf("departure must resolve the raised absence and clear the latch: %+v", d)
	}
	// A re-created token under the same key arms fresh and fires anew — proof the latch was cleared.
	e.SetExpected(key, at(20))
	e.Advance(at(31))
	if d := e.Drain(); len(d) != 1 || d[0].Edge != EdgeRaised || !d[0].At.Equal(at(30)) {
		t.Fatalf("a re-created device's dead-man must raise again after departure cleared the latch: %+v", d)
	}
}
