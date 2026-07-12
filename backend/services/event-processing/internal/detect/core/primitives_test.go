// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// TestWatermarkMonotonicAndLateness pins the two properties every timer/window firing leans
// on: the frontier holds `lateness` behind the max timestamp, and it never moves backward
// (so an out-of-order arrival can't rewind logical time).
func TestWatermarkMonotonicAndLateness(t *testing.T) {
	w := watermark{lateness: 5 * time.Second}
	if !w.observe(at(20)) {
		t.Fatal("first observe should advance the frontier")
	}
	if !w.now.Equal(at(15)) {
		t.Fatalf("frontier: want 15 (20-lateness), got %v", w.now)
	}
	if w.observe(at(18)) {
		t.Fatal("an older event must not move the frontier (18-5=13 < 15)")
	}
	if !w.now.Equal(at(15)) {
		t.Fatal("frontier moved backward")
	}
	if !w.observe(at(30)) || !w.now.Equal(at(25)) {
		t.Fatalf("forward event should advance to 25, got %v", w.now)
	}
}

// TestSlidingStateOutOfOrder proves the sliding window keeps buf time-sorted and the
// monotonic-deque min/max stay correct when samples arrive out of order (the rebuild path)
// and after time eviction.
func TestSlidingStateOutOfOrder(t *testing.T) {
	s := &slidingState{}
	for _, p := range []sample{{at(5), 3}, {at(1), 7}, {at(3), 1}, {at(4), 9}, {at(2), 5}} {
		s.insert(p)
	}
	for i := 1; i < len(s.buf); i++ {
		if s.buf[i].t.Before(s.buf[i-1].t) {
			t.Fatalf("buf not time-sorted at %d", i)
		}
	}
	assertAgg(t, s, 1, 9, 25, 5) // min max sum count over all five
	s.evict(at(3))               // drop t<=3 (values 7,1,5) -> remaining 9@4, 3@5
	assertAgg(t, s, 3, 9, 12, 2)
}

func assertAgg(t *testing.T, s *slidingState, min, max, sum, count float64) {
	t.Helper()
	if got := s.value(AggMin); got != min {
		t.Errorf("min: want %v got %v", min, got)
	}
	if got := s.value(AggMax); got != max {
		t.Errorf("max: want %v got %v", max, got)
	}
	if got := s.value(AggSum); got != sum {
		t.Errorf("sum: want %v got %v", sum, got)
	}
	if got := s.value(AggCount); got != count {
		t.Errorf("count: want %v got %v", count, got)
	}
}

// TestDeltaRateMode exercises the per-second rate branch of DeltaRate (the scenario covers
// raw delta): the same absolute change fires or not depending on the elapsed time.
func TestDeltaRateMode(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Rate: true, Op: GT, Thresh: 10}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "d"}
	feed := func(seq uint64, sec int, v float64) []Detection {
		e.ProcessEvent(Event{Seq: seq, Key: k, Time: at(sec), Value: v, Match: true})
		return e.Drain()
	}
	if d := feed(1, 0, 0); len(d) != 0 {
		t.Fatalf("prime should not fire, got %+v", d)
	}
	if d := feed(2, 4, 100); len(d) != 1 || d[0].Edge != EdgeRaised || d[0].At != at(4) { // 100/4s = 25/s > 10
		t.Fatalf("fast rate should raise @4, got %+v", d)
	}
	// The rate falls back below the threshold: the two-edge model resolves the raised alarm
	// (ADR-057) rather than emitting nothing.
	if d := feed(3, 14, 120); len(d) != 1 || d[0].Edge != EdgeResolved || d[0].At != at(14) { // 20/10s = 2/s < 10
		t.Fatalf("slow rate should resolve the prior raise @14, got %+v", d)
	}
}

// TestCorrelationMemberCap proves the per-anchor memory backstop bounds the retained member
// set deterministically to the newest MemberCap members.
func TestCorrelationMemberCap(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Correlation, Window: 100 * time.Second, Count: 3, MemberCap: 4}}
	e := NewEngine(rules, 0)
	anchor := SeriesKey{Rule: "r", Series: "area"}
	for i, m := range []string{"m0", "m1", "m2", "m3", "m4", "m5"} {
		e.ProcessEvent(Event{Seq: uint64(i + 1), Key: anchor, Member: m, Time: at(i), Match: true})
		e.Drain()
	}
	members := e.corr[anchor]
	if len(members) != 4 {
		t.Fatalf("member cap: want 4 retained, got %d", len(members))
	}
	for _, m := range []string{"m2", "m3", "m4", "m5"} { // oldest two (m0,m1) evicted
		if _, ok := members[m]; !ok {
			t.Errorf("expected newest member %s retained", m)
		}
	}
}

// TestTimerNoGenRecycle is the direct regression for the CRITICAL timer defect: a fired
// timer must BUMP its generation, never delete it, so a stale heap entry can't match a
// recycled gen and revive. The trace is the classic one — a deadline regression leaves a
// stale later-deadline timer behind, then the key fires and is rescheduled.
func TestTimerNoGenRecycle(t *testing.T) {
	w := newTimerWheel()
	k := SeriesKey{Rule: "r", Series: "s"}
	other := SeriesKey{Rule: "r", Series: "o"}
	fires := func(fired []firedTimer, key SeriesKey, dl time.Time) bool {
		for _, f := range fired {
			if f.key == key && f.deadline.Equal(dl) {
				return true
			}
		}
		return false
	}
	w.schedule(k, at(110))   // gen1 @110
	w.schedule(k, at(105))   // gen2 @105 (regression) — gen1@110 now stale but still in the heap
	w.schedule(other, at(0)) // unrelated, keeps the wheel non-trivial

	if f := w.popDue(at(106)); !fires(f, k, at(105)) {
		t.Fatalf("expected k to fire @105, got %+v", f)
	}
	w.schedule(k, at(117)) // reschedule: gen must be strictly higher, not recycled to 1

	if f := w.popDue(at(111)); fires(f, k, at(110)) {
		t.Fatal("stale timer @110 revived after a fire — generation was recycled")
	}
	if f := w.popDue(at(117)); !fires(f, k, at(117)) {
		t.Fatalf("real timer @117 was missed, got %+v", f)
	}
}

// TestSlidingAggSumRestoreExact proves the running SUM restores bit-for-bit, not by
// re-summing the buffer: eviction leaves float residue that a time-order re-sum can't
// reproduce, which would flip a threshold sitting exactly on the aggregate after a crash.
func TestSlidingAggSumRestoreExact(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggSum, Op: GT, Thresh: 1e9}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "s"}
	e.ProcessEvent(Event{Seq: 1, Key: k, Time: at(0), Value: 0.1, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: k, Time: at(1), Value: 0.2, Match: true})
	e.ProcessEvent(Event{Seq: 3, Key: k, Time: at(20), Value: 0.3, Match: true}) // evicts 0.1,0.2 → residue
	live := e.slides[k].sum

	snap, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Restore(rules, 0, snap)
	if err != nil {
		t.Fatal(err)
	}
	if r2.slides[k].sum != live {
		t.Fatalf("restored sum %v != live %v (Δ=%g) — sum was re-derived, not restored verbatim", r2.slides[k].sum, live, r2.slides[k].sum-live)
	}
}

// TestDeltaRateOutOfOrderIgnored proves a stale (older-timestamp) sample neither fires nor
// poisons the running last-value base for the next in-order sample.
func TestDeltaRateOutOfOrderIgnored(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "d"}
	feed := func(seq uint64, sec int, v float64) []Detection {
		e.ProcessEvent(Event{Seq: seq, Key: k, Time: at(sec), Value: v, Match: true})
		return e.Drain()
	}
	feed(1, 10, 100) // prime, base=(100,10)
	if d := feed(2, 20, 130); len(d) != 0 {
		t.Fatalf("+30 should not fire, got %+v", d)
	}
	if d := feed(3, 15, 1000); len(d) != 0 { // late sample: must be ignored, base stays (130,20)
		t.Fatalf("stale sample must not fire, got %+v", d)
	}
	// vs the correct base (130) this is +70 → fire; a base poisoned to 1000 would miss it.
	if d := feed(4, 25, 200); len(d) != 1 || d[0].At != at(25) {
		t.Fatalf("in-order +70 vs correct base should fire @25, got %+v", d)
	}
}

// TestCorrelationOutOfOrderKeepsLatest proves an out-of-order refresh doesn't regress a
// member's last-seen time and evict it early (which would let a later event double-fire).
func TestCorrelationOutOfOrderKeepsLatest(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Correlation, Window: 20 * time.Second, Count: 5, MemberCap: 100}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "area"}
	proc := func(seq uint64, m string, sec int) {
		e.ProcessEvent(Event{Seq: seq, Key: k, Member: m, Time: at(sec), Match: true})
		e.Drain()
	}
	proc(1, "A", 100)
	proc(2, "A", 118) // forward refresh → 118
	proc(3, "A", 105) // late refresh → must be ignored
	proc(4, "B", 135) // eviction pass, cutoff 115: A@118 survives; A regressed to 105 would evict
	if _, ok := e.corr[k]["A"]; !ok {
		t.Fatal("member A evicted early — an out-of-order refresh regressed its timestamp")
	}
}

// TestSessionOutOfOrderStampsClose proves a late in-session event doesn't pull the close
// deadline earlier: the session closes at (latest event time)+Gap, correctly stamped.
func TestSessionOutOfOrderStampsClose(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Session, Gap: 10 * time.Second, Agg: AggCount, Op: GE, Thresh: 3}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "s"}
	var all []Detection
	proc := func(seq uint64, sec int) {
		e.ProcessEvent(Event{Seq: seq, Key: k, Time: at(sec), Value: 1, Match: true})
		all = append(all, e.Drain()...)
	}
	proc(1, 100) // deadline 110
	proc(2, 105) // deadline 115 (latest event so far)
	proc(3, 102) // LATE: forward-only keeps deadline 115; count now 3
	e.Advance(at(120))
	all = append(all, e.Drain()...)
	if len(all) != 1 || all[0].At != at(115) {
		t.Fatalf("want one session close @115 (count 3), got %+v", all)
	}
}
