// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
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

// TestDeltaRateFallingEdgeOnNonMatch is the regression for review D2/D5 (DeltaRate): a filtering
// rule resolves on a non-matching sample rather than staying raised, and the non-match must NOT
// poison the delta base (which pairs consecutive MATCHING samples).
func TestDeltaRateFallingEdgeOnNonMatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 10}} // raw delta
	e := NewEngine(rules, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 0, true); len(d) != 0 {
		t.Fatalf("prime must not fire: %+v", d)
	}
	if d := feedEvent(e, 2, "r", "d", 2, 100, true); len(d) != 1 || d[0].Edge != EdgeRaised { // +100 > 10
		t.Fatalf("a +100 delta must raise: %+v", d)
	}
	// A non-matching sample (the filtering leaf went false) resolves the raised alarm.
	if d := feedEvent(e, 3, "r", "d", 5, 999, false); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(5)) {
		t.Fatalf("a non-matching sample must resolve the raise: %+v", d)
	}
	// The non-match did NOT become the delta base: a following match pairs against the last MATCHING
	// value (100@t2), so +5 (=105-100) does NOT re-raise; a base poisoned to 999 would (105-999<0).
	if d := feedEvent(e, 4, "r", "d", 8, 105, true); len(d) != 0 {
		t.Fatalf("delta base must stay on the last matching sample (100), so +5 must not re-raise: %+v", d)
	}
}

// TestAggregateFallingEdgeOnNonMatch is the regression for review D2/D5 (Aggregate): a raised
// Aggregate alarm resolves under active non-matching traffic — a non-matching event opens an empty
// pane for the current window, which closes on the watermark and (empty-never-satisfies) resolves.
func TestAggregateFallingEdgeOnNonMatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Aggregate, Window: 10 * time.Second, Agg: AggCount, Op: GT, Thresh: 1}}
	e := NewEngine(rules, 0)
	drain := func() []Detection { return e.Drain() }
	feed := func(seq uint64, sec int, match bool) []Detection {
		e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(sec), Value: 1, HasValue: true, Match: match})
		return drain()
	}
	feed(1, 1, true) // pane [0,10): count 1
	feed(2, 2, true) // pane [0,10): count 2
	e.Advance(at(12))
	if d := drain(); len(d) != 1 || d[0].Edge != EdgeRaised || !d[0].At.Equal(at(10)) { // count 2 > 1
		t.Fatalf("a satisfied pane close must raise@10: %+v", d)
	}
	// Device now reports only non-matching values. A non-match@15 (while raised) opens an EMPTY pane
	// for [10,20); it must fold no value (count stays 0).
	if d := feed(3, 15, false); len(d) != 0 {
		t.Fatalf("a non-matching event must not itself emit: %+v", d)
	}
	e.Advance(at(22))
	if d := drain(); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(20)) {
		t.Fatalf("the empty pane closing must resolve the raised alarm@20: %+v", d)
	}
}

// TestSlidingAggNonMatchDoesNotRaise pins the option-a decision from the 6d-pre-2a review: a
// SlidingAgg rising edge is match-only. Eviction on a non-matching sample can push the aggregate INTO
// satisfaction, but that raise is DEFERRED to the next matching sample (symmetric with
// Repeating/Correlation, whose rising edge is structurally match-only) rather than raising on the
// non-match — which would be ragged (unreachable once the window empties and the entry is deleted).
func TestSlidingAggNonMatchDoesNotRaise(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggAvg, Op: GT, Thresh: 100}}
	e := NewEngine(rules, 0)
	if d := feedEvent(e, 1, "r", "d", 0, 20, true); len(d) != 0 { // avg 20, not > 100
		t.Fatalf("first low sample must not raise: %+v", d)
	}
	if d := feedEvent(e, 2, "r", "d", 7, 150, true); len(d) != 0 { // avg (20+150)/2 = 85, still not > 100
		t.Fatalf("avg 85 must not raise: %+v", d)
	}
	// A non-match@12 evicts 20@t0: window {150@t7}, avg 150 > 100 satisfies — but the rising edge is
	// match-only, so NO raise is emitted (deferred).
	if d := feedEvent(e, 3, "r", "d", 12, 999, false); len(d) != 0 {
		t.Fatalf("eviction into satisfaction on a NON-match must not raise (deferred to next match): %+v", d)
	}
	// The next matching sample observes the satisfied window and raises.
	if d := feedEvent(e, 4, "r", "d", 13, 150, true); len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("the next matching sample must raise the (now satisfied) window: %+v", d)
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
// every (checkpoint, crash) pair over a filtering-`when` scenario whose falling edges arrive on
// NON-matching events across all five affected kinds (Repeating, SlidingAgg, Correlation, DeltaRate,
// Aggregate), and proves BOTH (a) the dedup-collapsed union of the crashed+replayed run exactly equals
// the clean run — no missed and no spurious edge — and (b) the replayed engine's FINAL snapshot is
// byte-identical to the clean run's, so no silent state divergence hides inside the trace. The raised
// latch, sliding buffers, cohort maps, delta state, and tumbling panes are all snapshotted, so every
// new resolve edge survives checkpoint/crash.
func TestMatchGatedFallingEdgeReplayCorrect(t *testing.T) {
	rules := []Rule{
		{ID: "rRep", Kind: Repeating, Window: 10 * time.Second, Count: 2},
		{ID: "rSlide", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 100},
		{ID: "rCorr", Kind: Correlation, Window: 10 * time.Second, Count: 2, MemberCap: 10},
		{ID: "rDelta", Kind: DeltaRate, Op: GT, Thresh: 10},
		{ID: "rAgg", Kind: Aggregate, Window: 10 * time.Second, Agg: AggCount, Op: GT, Thresh: 1},
	}
	corrStep := func(seq uint64, member string, sec int, match bool) step {
		return step{ev: &Event{Seq: seq, Key: SeriesKey{Rule: "rCorr", Series: "area"}, Member: member, Time: at(sec), Match: match}}
	}
	steps := []step{
		evValStep(1, "rRep", "d", 0, 1),      // match
		evValStep(2, "rRep", "d", 1, 1),      // match -> Repeating raise
		evValStep(3, "rSlide", "s", 1, 150),  // match -> SlidingAgg raise (sum 150 > 100)
		evValStep(4, "rDelta", "e", 0, 0),    // prime
		evValStep(5, "rDelta", "e", 2, 100),  // match -> DeltaRate raise (+100 > 10)
		corrStep(6, "m0", 0, true),           // first distinct member
		corrStep(7, "m1", 1, true),           // second distinct member -> Correlation raise
		evValStep(8, "rAgg", "f", 1, 1),      // pane [0,10) count 1
		evValStep(9, "rAgg", "f", 2, 1),      // pane [0,10) count 2
		advStep(12),                          // close pane -> Aggregate raise@10 (2 > 1)
		evStep(11, "rRep", "d", 13, false),   // non-match ages the burst out -> Repeating resolve
		evStep(12, "rSlide", "s", 14, false), // non-match ages the window out -> SlidingAgg resolve
		evStep(13, "rDelta", "e", 15, false), // non-match -> DeltaRate resolve
		corrStep(14, "m2", 16, false),        // non-match ages the cohort out -> Correlation resolve
		evStep(15, "rAgg", "f", 17, false),   // non-match while raised opens an empty pane [10,20)
		advStep(22),                          // close empty pane -> Aggregate resolve@20
	}
	want := dedup(runClean(rules, steps))

	// The clean run's final state — the byte target every replayed run must land on.
	clean := NewEngine(rules, 0)
	for _, s := range steps {
		applyStep(clean, s)
		clean.Drain()
	}
	cleanSnap, err := clean.Snapshot()
	if err != nil {
		t.Fatalf("clean snapshot: %v", err)
	}

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

				// No silent state divergence: the replayed engine must end byte-identical to clean.
				got, err := e.Snapshot()
				if err != nil {
					t.Fatalf("final snapshot: %v", err)
				}
				if !bytes.Equal(got, cleanSnap) {
					t.Errorf("final snapshot diverged from clean run:\n got: %s\nwant: %s", got, cleanSnap)
				}
			})
		}
	}
}

// TestSlidingAggAtRegressionReplay covers the out-of-order At-regression the D2/D5 falling edge makes
// newly reachable for the sliding kinds: a Resolved is emitted, then a bounded-late matching event
// reopens a fresh window and stamps a Raised at an EARLIER event time than the Resolved. Both edges
// are dedup-distinct and must survive every (checkpoint, crash) pair — the alarm object's monotonic
// decision-time guard (not arrival order) is what orders them downstream.
func TestSlidingAggAtRegressionReplay(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 100}}
	steps := []step{
		evValStep(1, "r", "d", 10, 150), // match -> raise@10 (sum 150 > 100)
		evStep(2, "r", "d", 22, false),  // non-match ages the window out -> resolve@22
		evValStep(3, "r", "d", 15, 150), // bounded-late match@15 (< 22) reopens window -> raise@15
	}
	want := dedup(runClean(rules, steps))
	n := len(steps)
	for checkpoint := 0; checkpoint < n; checkpoint++ {
		for crash := checkpoint; crash < n; crash++ {
			e := NewEngine(rules, 30*time.Second) // lateness admits the out-of-order match@15
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
			e, err := Restore(rules, 30*time.Second, snap)
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			for idx := checkpoint + 1; idx < n; idx++ {
				applyStep(e, steps[idx])
				all = append(all, e.Drain()...)
			}
			assertSetEqual(t, want, dedup(all))
		}
	}
	// Sanity: the clean run really does produce the At-regression (raise@15 after resolve@22).
	got := runClean(rules, steps)
	var sawResolve22, sawRaise15After bool
	for _, d := range got {
		if d.Edge == EdgeResolved && d.At.Equal(at(22)) {
			sawResolve22 = true
		}
		if d.Edge == EdgeRaised && d.At.Equal(at(15)) && sawResolve22 {
			sawRaise15After = true
		}
	}
	if !sawRaise15After {
		t.Fatalf("expected a raise@15 emitted AFTER resolve@22 (the At-regression): %+v", got)
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
