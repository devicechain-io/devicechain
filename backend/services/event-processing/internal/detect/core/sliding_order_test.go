// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"math/rand"
	"testing"
	"time"
)

// The invariant the monotonic deques exist to provide, and the one an incremental splice or a
// deferred rebuild could quietly break: the window's aggregates depend on WHICH samples are in
// it, never on the order they arrived in.
//
// Every value here is a small integer, so the sums are exact and can be compared with ==.
// That is deliberate and not incidental: float addition is not associative, so a window built
// from real-valued samples in two different orders may legitimately differ in the last ulp
// (restoreSlides carries the same caveat about re-deriving a sum). Pinning ORDER-INDEPENDENCE
// and pinning FLOAT-ASSOCIATIVITY are different claims, and only the first one is true.

// aggsOf reads all five aggregates off a window.
func aggsOf(s *slidingState) [5]float64 {
	return [5]float64{s.value(AggMin), s.value(AggMax), s.value(AggSum), s.value(AggAvg), s.value(AggCount)}
}

// orderTestSamples returns n samples in time order, with values that rise, fall and repeat so
// the running min and max both change hands repeatedly and ties are exercised.
func orderTestSamples(n int) []sample {
	xs := make([]sample, n)
	for i := range xs {
		xs[i] = sample{t: at(i), v: float64((i*7)%11) - 5}
	}
	return xs
}

// TestSlidingAggIsOrderIndependentAtEveryStep builds the same window two ways for every prefix
// length — once in time order (the append fast path) and once in a shuffled order (the splice
// path) — and requires all five aggregates to agree at every step.
func TestSlidingAggIsOrderIndependentAtEveryStep(t *testing.T) {
	xs := orderTestSamples(40)
	r := rand.New(rand.NewSource(7))
	for k := 1; k <= len(xs); k++ {
		inOrder := &slidingState{}
		for _, x := range xs[:k] {
			inOrder.insert(x)
		}
		want := aggsOf(inOrder)
		for trial := 0; trial < 20; trial++ {
			perm := append([]sample(nil), xs[:k]...)
			r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			shuffled := &slidingState{}
			for _, x := range perm {
				shuffled.insert(x)
			}
			if got := aggsOf(shuffled); got != want {
				t.Fatalf("k=%d trial=%d: shuffled arrival gave %v, in-order gave %v (order: %v)", k, trial, got, want, perm)
			}
			assertRunsSorted(t, shuffled, k, trial)
		}
	}
}

// TestSplicedDequesEqualARebuild is the stronger claim underneath the one above: after an
// out-of-order insert the deques are not merely good enough to answer min and max, they are
// BYTE-IDENTICAL to what rebuilding from buf would have produced. That is what lets a later
// rebuild (a restore, an eviction, a snapshot round trip) be a no-op rather than a correction —
// an "equivalent enough" deque would answer min correctly today and wrongly after one eviction
// dropped the entry it was leaning on.
func TestSplicedDequesEqualARebuild(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for trial := 0; trial < 200; trial++ {
		s := &slidingState{}
		n := 1 + r.Intn(60)
		for i := 0; i < n; i++ {
			x := sample{t: at(r.Intn(20)), v: float64(r.Intn(9) - 4)} // dense ties in BOTH time and value
			s.insert(x)
			assertDequesMatchRebuild(t, s, "", trial, i)
			assertRunsSorted(t, s, trial, i)
			assertOverlayInvariant(t, s, trial, i)
			if r.Intn(4) == 0 { // interleave evictions: the fronts are what evict trims
				s.evict(at(r.Intn(20)))
				assertDequesMatchRebuild(t, s, " after evict", trial, i)
				assertRunsSorted(t, s, trial, i)
				assertOverlayInvariant(t, s, trial, i)
			}
		}
	}
}

// assertDequesMatchRebuild compares the incrementally maintained deques against a rebuild of
// the SAME window — which means copying the overlay too, because the window is buf AND late.
// That makes this one assertion a cross-check of two independent things: the splice, and
// mergeLate's tie order. If the merge put a late sample on the wrong side of an
// equal-timestamped one already held, the rebuild would disagree with the splice right here.
func assertDequesMatchRebuild(t *testing.T, s *slidingState, when string, trial, step int) {
	t.Helper()
	ref := &slidingState{
		buf:  append([]sample(nil), s.buf...),
		late: append([]sample(nil), s.late...),
	}
	ref.rebuildDeques()
	assertSameDeque(t, "minDq"+when, s.minDq, ref.minDq, trial, step)
	assertSameDeque(t, "maxDq"+when, s.maxDq, ref.maxDq, trial, step)
}

// assertRunsSorted pins the structural invariant the overlay rests on: EACH run is
// independently time-sorted. Eviction trims a prefix of each without merging, so a run that
// lost its order would drop live samples and keep expired ones — silently, since the count
// and the sum would still add up.
func assertRunsSorted(t *testing.T, s *slidingState, trial, step int) {
	t.Helper()
	for name, run := range map[string][]sample{"buf": s.buf, "late": s.late} {
		for i := 1; i < len(run); i++ {
			if run[i].t.Before(run[i-1].t) {
				t.Fatalf("trial %d step %d: %s not time-sorted at %d: %v", trial, step, name, i, run)
			}
		}
	}
}

func assertSameDeque(t *testing.T, name string, got, want []sample, trial, step int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("trial %d step %d: %s length %d, rebuild says %d\n got %v\nwant %v", trial, step, name, len(got), len(want), got, want)
	}
	for i := range got {
		if !got[i].t.Equal(want[i].t) || got[i].v != want[i].v {
			t.Fatalf("trial %d step %d: %s[%d] = %v, rebuild says %v\n got %v\nwant %v", trial, step, name, i, got[i], want[i], got, want)
		}
	}
}

// TestRestoredWindowDefersItsDequesSafely covers the one state that is deliberately allowed to
// hold stale deques: a window restored from a snapshot owes a rebuild rather than doing one.
// The debt has to survive every operation that can happen before the first min/max read, which
// is the whole risk of deferring it — an evict or an out-of-order insert that trimmed or spliced
// the stale deque would corrupt it beyond what the eventual rebuild could see.
func TestRestoredWindowDefersItsDequesSafely(t *testing.T) {
	restore := func() *slidingState {
		e := NewEngine([]Rule{{ID: "r", Kind: SlidingAgg, Agg: AggMin, Window: time.Hour, Op: LT, Thresh: -99}}, 0)
		e.restoreSlides([]snapSlide{{
			Rule:   "r",
			Series: "d",
			Times:  []time.Time{at(1), at(2), at(3), at(4)},
			Values: []float64{5, 2, 9, 7},
			Sum:    23,
		}})
		st := e.slides[SeriesKey{Rule: "r", Series: "d"}]
		if !st.dirty {
			t.Fatal("a restored window should owe its deques, not hold them")
		}
		return st
	}

	t.Run("read straight after restore", func(t *testing.T) {
		st := restore()
		if got := aggsOf(st); got != [5]float64{2, 9, 23, 23.0 / 4, 4} {
			t.Fatalf("got %v", got)
		}
		// The debt is paid ONCE. Without this the flag could stay set forever and every
		// subsequent min/max read would silently rebuild the whole window — correct, and
		// exactly the per-event O(len(buf)) cost this change exists to remove.
		if st.dirty {
			t.Fatal("a read should have paid the deque debt, not re-owed it")
		}
	})

	t.Run("evict before the first read", func(t *testing.T) {
		st := restore()
		st.evict(at(2)) // drops 5@1 and 2@2 — including the stale deque's min
		if got := aggsOf(st); got != [5]float64{7, 9, 16, 8, 2} {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("out-of-order insert before the first read", func(t *testing.T) {
		st := restore()
		st.insert(sample{t: at(2), v: -1}) // lands mid-buffer, and is the new min
		if !st.dirty {
			t.Fatal("an insert into a dirty window must not pretend to have cleaned it")
		}
		if got := aggsOf(st); got != [5]float64{-1, 9, 22, 22.0 / 5, 5} {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("in-order insert before the first read", func(t *testing.T) {
		st := restore()
		st.insert(sample{t: at(9), v: -3})
		if got := aggsOf(st); got != [5]float64{-3, 9, 20, 4, 5} {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("snapshot does not observe the deques", func(t *testing.T) {
		st := restore()
		e := NewEngine(nil, 0)
		e.slides[SeriesKey{Rule: "r", Series: "d"}] = st
		out := e.snapshotSlides()
		if len(out) != 1 || len(out[0].Times) != 4 || out[0].Sum != 23 {
			t.Fatalf("a dirty window must round-trip verbatim, got %+v", out)
		}
		if !st.dirty {
			t.Fatal("snapshotting should not have paid the deque debt")
		}
	})
}

// assertOverlayInvariant pins what insert's fast path is ALLOWED to assume: every overlay
// sample is strictly older than buf's last, and buf is empty only when late is too.
//
// 🔴 This is a test for a check that is NOT in the production code, and that is the point.
// insert extends the deques for anything it can append to buf, without testing the overlay —
// correct only because an appendable sample is thereby at or after everything held. A guard
// there would be unkillable (it can never fire), so the claim is asserted here instead, where
// a change to eviction that broke it would fail rather than silently corrupt a running min.
func assertOverlayInvariant(t *testing.T, s *slidingState, trial, step int) {
	t.Helper()
	if len(s.late) == 0 {
		return
	}
	if len(s.buf) == 0 {
		t.Fatalf("trial %d step %d: overlay held %d samples with an EMPTY buf — insert would "+
			"then append an older sample and extend the deques with it", trial, step, len(s.late))
	}
	if newest, bufLast := s.late[len(s.late)-1].t, s.buf[len(s.buf)-1].t; !newest.Before(bufLast) {
		t.Fatalf("trial %d step %d: overlay's newest (%v) is not before buf's last (%v)",
			trial, step, newest, bufLast)
	}
}

// TestOverlayIsInvisibleToEveryAggregate is the overlay's whole contract: a window carrying an
// unmerged overlay must answer exactly as the same window merged. The merge is triggered by a
// size threshold, so without this the aggregates would be correct only at the moments a merge
// happened to have just run.
func TestOverlayIsInvisibleToEveryAggregate(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for trial := 0; trial < 400; trial++ {
		s := &slidingState{}
		for i := 0; i < 1+r.Intn(80); i++ {
			s.insert(sample{t: at(r.Intn(25)), v: float64(r.Intn(11) - 5)})
		}
		if len(s.late) == 0 {
			continue // nothing deferred; this trial says nothing
		}
		open := aggsOf(s)
		// dirty, so the reference DERIVES its deques from the merged window rather than
		// inheriting the ones under test — otherwise this would compare a window against
		// its own answer.
		merged := &slidingState{
			buf:   append([]sample(nil), s.buf...),
			late:  append([]sample(nil), s.late...),
			sum:   s.sum,
			dirty: true,
		}
		merged.mergeLate()
		if got := aggsOf(merged); got != open {
			t.Fatalf("trial %d: merged window answers %v, the same window with an open overlay answers %v",
				trial, got, open)
		}
	}
}

// TestMergeKeepsBufFirstOnATie pins the tie rule spliceDeque already assumes. It is the one
// choice in mergeLate that is invisible until timestamps collide, and getting it backwards
// makes the deques stop matching a rebuild for exactly those samples.
func TestMergeKeepsBufFirstOnATie(t *testing.T) {
	s := &slidingState{
		buf:  []sample{{at(1), 10}, {at(5), 50}, {at(9), 90}},
		late: []sample{{at(5), 55}, {at(7), 70}},
	}
	s.mergeLate()
	want := []sample{{at(1), 10}, {at(5), 50}, {at(5), 55}, {at(7), 70}, {at(9), 90}}
	if len(s.late) != 0 {
		t.Fatalf("the overlay should be empty after a merge, got %v", s.late)
	}
	assertSameDeque(t, "merged window", s.buf, want, 0, 0)
}

// TestEvictionSpansBothRuns covers the change eviction actually underwent: it trims a prefix
// of EACH run rather than one merged sequence. A version that forgot the overlay would keep
// expired samples in the window and subtract nothing for them from the sum — a window that
// never fully ages out, with a sum that no longer matches its own contents.
func TestEvictionSpansBothRuns(t *testing.T) {
	s := &slidingState{}
	for _, x := range []sample{{at(2), 2}, {at(4), 4}, {at(6), 6}, {at(8), 8}} {
		s.insert(x)
	}
	s.insert(sample{t: at(3), v: 3}) // into the overlay
	s.insert(sample{t: at(7), v: 7}) // into the overlay
	if len(s.late) != 2 {
		t.Fatalf("expected two deferred samples, got %v", s.late)
	}
	s.evict(at(4)) // drops 2@2 and 4@4 from buf, and 3@3 from the overlay
	if got := aggsOf(s); got != [5]float64{6, 8, 21, 7, 3} {
		t.Fatalf("after eviction across both runs: got %v, want min 6 max 8 sum 21 avg 7 count 3", got)
	}
	// And the sum must be the sum of what is actually left, not an accumulator that drifted.
	var total float64
	for _, run := range [][]sample{s.buf, s.late} {
		for _, x := range run {
			total += x.v
		}
	}
	if total != s.sum {
		t.Fatalf("running sum %v does not match the retained samples (%v)", s.sum, total)
	}
}

// TestSnapshotFoldsAnOpenOverlay: a snapshot is the window as ONE ordered array, so it is one
// of only two places that must merge first. Writing buf alone would drop every deferred
// sample; writing them concatenated would restore an unsorted window, which eviction reads as
// "nothing has expired" and the deque rebuild reads as a different min.
func TestSnapshotFoldsAnOpenOverlay(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: SlidingAgg, Agg: AggMin, Window: time.Hour, Op: LT, Thresh: -99}}, 0)
	key := SeriesKey{Rule: "r", Series: "d"}
	st := &slidingState{}
	for _, x := range []sample{{at(1), 1}, {at(3), 3}, {at(5), 5}, {at(9), 9}} {
		st.insert(x)
	}
	st.insert(sample{t: at(4), v: 4})
	st.insert(sample{t: at(2), v: 2})
	e.slides[key] = st

	out := e.snapshotSlides()
	if len(out) != 1 {
		t.Fatalf("want one series, got %d", len(out))
	}
	if len(out[0].Times) != 6 {
		t.Fatalf("the snapshot must carry every retained sample, got %d of 6", len(out[0].Times))
	}
	for i := 1; i < len(out[0].Times); i++ {
		if out[0].Times[i].Before(out[0].Times[i-1]) {
			t.Fatalf("snapshot not time-ordered at %d: %v", i, out[0].Times)
		}
	}

	// Round trip: the restored window must answer identically.
	before := aggsOf(st)
	e2 := NewEngine(nil, 0)
	e2.restoreSlides(out)
	if got := aggsOf(e2.slides[key]); got != before {
		t.Fatalf("restored window answers %v, the live one answered %v", got, before)
	}
}

// TestOverlayStaysBounded is the performance property stated as a behaviour, because nothing
// else in this suite can see it. Deferring the merge is only a fix while the overlay is
// SMALL: a version that never merged would answer every aggregate correctly and quietly
// return to a memmove of the whole overlay per late sample — the quadratic this replaced,
// wearing a different name. So the bound is asserted, not assumed.
//
// The numbers are chosen independently of the trigger: 5000 late samples into a
// 10 000-deep window. An overlay that never merged would hold ~5000.
func TestOverlayStaysBounded(t *testing.T) {
	const depth, batch, bound = 10000, 5000, 200
	s := &slidingState{}
	for i := 0; i < depth; i++ {
		s.insert(sample{t: at(i), v: float64(i % 977)})
	}
	r := rand.New(rand.NewSource(5))
	worst := 0
	for i := 0; i < batch; i++ {
		s.insert(sample{t: at(depth/4 + r.Intn(depth/2)), v: float64(r.Intn(977))})
		if len(s.late) > worst {
			worst = len(s.late)
		}
	}
	if worst > bound {
		t.Fatalf("the overlay grew to %d samples; it must stay under %d (an overlay that never "+
			"merged would reach ~%d, and each insert into it is a memmove of the whole thing)",
			worst, bound, batch)
	}
	if worst == 0 {
		t.Fatal("no sample was ever deferred — this test measured nothing")
	}
	if got := s.count(); got != depth+batch {
		t.Fatalf("the window holds %d samples, want %d — merging must not lose any", got, depth+batch)
	}
}

// TestRetainedSampleGaugeSeesTheOverlay: the state-budget gauge exists to make a window
// overrun visible, and a store-and-forward burst is exactly when an overlay is open. Reading
// buf alone would under-report by the whole deferred batch at the one moment the number is
// being watched.
func TestRetainedSampleGaugeSeesTheOverlay(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: SlidingAgg, Agg: AggMin, Window: time.Hour, Op: LT, Thresh: -99}}, 0)
	st := &slidingState{}
	for i := 0; i < 40; i++ {
		st.insert(sample{t: at(i * 2), v: float64(i)})
	}
	for i := 0; i < 7; i++ { // odd instants, all behind the last — every one deferred
		st.insert(sample{t: at(i*2 + 1), v: float64(i)})
	}
	if len(st.late) == 0 {
		t.Fatal("expected an open overlay; this test would otherwise measure nothing")
	}
	e.slides[SeriesKey{Rule: "r", Series: "d"}] = st
	if got := e.RetainedSampleCounts()["r"]; got != 47 {
		t.Fatalf("gauge reports %d retained samples, want 47 (%d folded in + %d deferred)",
			got, len(st.buf), len(st.late))
	}
}
