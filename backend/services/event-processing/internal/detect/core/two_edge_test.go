// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
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
