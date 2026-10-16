// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// The sample-pairing kinds — DeltaRate (consecutive samples) and CountWindow (every N samples) —
// are defined over a series' samples in EVENT-TIME order, so the detections they emit must be a
// function of WHICH samples arrived, never of the order they arrived in. N inbound readers in
// device-management, a JetStream redelivery, a batched upload: each presents the same samples in a
// different order, and a kind that pairs or groups by arrival answers differently for each.
//
// Each case feeds one message per sample through the REAL path (ProcessResolved, which advances the
// watermark from the message time as the live processor does), with a lateness budget wide enough
// that every permutation is inside it, then advances past everything so every sample is final. The
// two drains must be identical, by value.

// feedPermutation feeds samples in the given order (each as its own message, sequences in arrival
// order), advances the frontier past them all, and returns every detection emitted plus the final
// snapshot.
func feedPermutation(rules []Rule, lateness time.Duration, key SeriesKey, samples []Event, order []int) ([]Detection, []byte) {
	e := NewEngine(rules, lateness)
	var all []Detection
	for i, idx := range order {
		ev := samples[idx]
		ev.Key = key
		ev.Seq = uint64(i + 1)
		e.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
		all = append(all, e.Drain()...)
	}
	e.Advance(at(10_000))
	all = append(all, e.Drain()...)
	snap, err := e.Snapshot()
	if err != nil {
		panic(err)
	}
	return all, snap
}

func TestDeltaRateIsArrivalOrderIndependent(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}
	key := SeriesKey{Rule: "r", Series: "dev"}
	samples := []Event{
		{Time: at(1), Value: 0, HasValue: true, Match: true},
		{Time: at(2), Value: 100, HasValue: true, Match: true}, // +100 against @1: the rising edge
		{Time: at(3), Value: 100, HasValue: true, Match: true}, // +0 against @2: the falling edge
	}
	want, _ := feedPermutation(rules, 10*time.Second, key, samples, []int{0, 1, 2})
	got, _ := feedPermutation(rules, 10*time.Second, key, samples, []int{0, 2, 1})
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("arrival order changed the DeltaRate output\n in order: %+v\n permuted: %+v", want, got)
	}
	if len(want) != 2 || want[0].Edge != EdgeRaised || want[0].Value != 100 || want[1].Edge != EdgeResolved {
		t.Fatalf("the in-order run must raise on +100 and resolve on +0, got %+v", want)
	}
}

func TestCountWindowIsArrivalOrderIndependent(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: CountWindow, Count: 2, Agg: AggSum, Op: GT, Thresh: 150}}
	key := SeriesKey{Rule: "r", Series: "dev"}
	samples := []Event{
		{Time: at(1), Value: 100, HasValue: true, Match: true},
		{Time: at(2), Value: 100, HasValue: true, Match: true}, // window {1,2} sums 200: raise
		{Time: at(3), Value: 0, HasValue: true, Match: true},
		{Time: at(4), Value: 0, HasValue: true, Match: true}, // window {3,4} sums 0: resolve
	}
	want, _ := feedPermutation(rules, 10*time.Second, key, samples, []int{0, 1, 2, 3})
	got, _ := feedPermutation(rules, 10*time.Second, key, samples, []int{0, 2, 1, 3})
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("arrival order changed the CountWindow output\n in order: %+v\n permuted: %+v", want, got)
	}
	if len(want) != 2 || want[0].Edge != EdgeRaised || want[0].Value != 200 || want[1].Edge != EdgeResolved {
		t.Fatalf("the in-order run must raise on {1,2} and resolve on {3,4}, got %+v", want)
	}
}

// TestSamplePairingKindsAreArrivalOrderIndependent is the property behind the two cases above:
// random sample sets — dense ties in time, non-matching samples mixed in, both DeltaRate modes and
// two CountWindow shapes — fed in time order and in a random permutation give the same detections
// AND the same final snapshot. Every timestamp lies inside the lateness budget, which is the
// contract: within the budget the output is a function of the set.
func TestSamplePairingKindsAreArrivalOrderIndependent(t *testing.T) {
	kinds := []struct {
		name string
		rule Rule
	}{
		{"delta", Rule{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}},
		{"rate", Rule{ID: "r", Kind: DeltaRate, Rate: true, Op: GT, Thresh: 10}},
		{"count-sum", Rule{ID: "r", Kind: CountWindow, Count: 3, Agg: AggSum, Op: GT, Thresh: 150}},
		{"count-max-lt", Rule{ID: "r", Kind: CountWindow, Count: 2, Agg: AggMax, Op: LT, Thresh: 30}},
	}
	key := SeriesKey{Rule: "r", Series: "dev"}
	for _, k := range kinds {
		r := rand.New(rand.NewSource(int64(len(k.name))))
		raised, trials := 0, 0
		for trial := 0; trial < 300; trial++ {
			n := 2 + r.Intn(8)
			samples := make([]Event, n)
			for i := range samples {
				samples[i] = Event{Time: at(1 + r.Intn(6)), Value: float64(r.Intn(101)), HasValue: true, Match: r.Intn(5) != 0}
			}
			inOrder := make([]int, n)
			for i := range inOrder {
				inOrder[i] = i
			}
			sort.SliceStable(inOrder, func(a, b int) bool {
				x, y := samples[inOrder[a]], samples[inOrder[b]]
				return pendingSample{t: x.Time, v: x.Value, match: x.Match}.before(pendingSample{t: y.Time, v: y.Value, match: y.Match})
			})
			want, wantSnap := feedPermutation([]Rule{k.rule}, 100*time.Second, key, samples, inOrder)
			perm := append([]int(nil), inOrder...)
			r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			got, gotSnap := feedPermutation([]Rule{k.rule}, 100*time.Second, key, samples, perm)
			trials++
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s trial %d: arrival order changed the output\n samples: %v\n order: %v\n in order: %+v\n permuted: %+v",
					k.name, trial, describeSamples(samples), perm, want, got)
			}
			if !bytes.Equal(gotSnap, wantSnap) {
				t.Fatalf("%s trial %d: arrival order changed the final state\n in order: %s\n permuted: %s", k.name, trial, wantSnap, gotSnap)
			}
			for _, d := range want {
				if d.Edge == EdgeRaised {
					raised++
				}
			}
		}
		if raised < 20 {
			t.Fatalf("%s: only %d raises across %d trials — the generator barely exercises the kind", k.name, raised, trials)
		}
	}
}

func describeSamples(evs []Event) string {
	s := ""
	for _, e := range evs {
		s += fmt.Sprintf("(%ds v=%v m=%v) ", int(e.Time.Sub(base).Seconds()), e.Value, e.Match)
	}
	return s
}

// TestHeldSampleIsPlacedByItsOwnTimeBehindTheFrontier pins the admission rule: a sample behind the
// FRONTIER but after the series' newest FINAL sample is not late — it is slotted by its own time
// among the samples still held, exactly as if it had arrived in order. This is the budget that
// keeps a device whose clock trails the fleet by more than the tolerance working, the same
// reasoning applyDuration gives, and it is what a frontier-based admission would break: with
// lateness 0 the frontier is AT the newest sample, so every out-of-order reading would be refused.
func TestHeldSampleIsPlacedByItsOwnTimeBehindTheFrontier(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}
	key := SeriesKey{Rule: "r", Series: "dev"}
	samples := []Event{
		{Time: at(10), Value: 0, HasValue: true, Match: true},
		{Time: at(15), Value: 100, HasValue: true, Match: true}, // +100 vs @10: raise@15
		{Time: at(20), Value: 100, HasValue: true, Match: true}, // +0 vs @15: resolve@20
	}
	// With NO lateness the frontier sits on @20 when @15 arrives: @15 is behind the frontier but
	// after the final base (@10), so it is held and placed before @20.
	want, _ := feedPermutation(rules, 0, key, samples, []int{0, 1, 2})
	got, _ := feedPermutation(rules, 0, key, samples, []int{0, 2, 1})
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("a sample behind the frontier but after the base must be placed by its own time\n in order: %+v\n permuted: %+v", want, got)
	}
	if len(want) != 2 || !want[0].At.Equal(at(15)) || !want[1].At.Equal(at(20)) {
		t.Fatalf("want raise@15 and resolve@20, got %+v", want)
	}
	if n := NewEngine(rules, 0).lateSamples; n != 0 {
		t.Fatalf("fresh engine late count %d", n)
	}
}

// TestSamplePairingLatePolicy pins what happens to a sample older than the series' newest FINAL
// sample — the disposition the other kinds already make. A match is refused and counted; a
// DeltaRate non-match is a falling edge and resolves an alarm it postdates (and is not counted);
// one older than the raise is ignored by resolve's own stale guard.
func TestSamplePairingLatePolicy(t *testing.T) {
	t.Run("delta: a late match is refused and counted, the base is untouched", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}, 0)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
		e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 10, HasValue: true, Match: true})
		e.Advance(at(21)) // both final: base = (10, @20)
		e.Drain()
		e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(15), Value: 1000, HasValue: true, Match: true})
		if n := e.DrainLateSamples(); n != 1 {
			t.Fatalf("late count %d, want 1", n)
		}
		if len(e.pending) != 0 {
			t.Fatal("a refused sample must not be held")
		}
		if base := e.lastVal[key]; base.value != 10 || !base.time.Equal(at(20)) {
			t.Fatalf("base moved to %+v", base)
		}
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("a refused sample must emit nothing, got %+v", d)
		}
	})

	t.Run("delta: a late non-match resolves an alarm it postdates, and is not counted", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}, 0)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
		e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true}) // raise@20
		e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(30), Value: 200, HasValue: true, Match: true}) // +100: stays raised; base @30
		e.Advance(at(31))
		if d := e.Drain(); len(d) != 1 || d[0].Edge != EdgeRaised || !d[0].At.Equal(at(20)) {
			t.Fatalf("want one raise@20, got %+v", d)
		}
		// Older than the raise: resolve's stale guard ignores it.
		e.ProcessEvent(Event{Seq: 4, Key: key, Time: at(15), Value: 0, HasValue: true, Match: false})
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("a non-match older than the raise must not resolve it, got %+v", d)
		}
		// Older than the base but after the raise: the falling edge, honoured however late.
		e.ProcessEvent(Event{Seq: 5, Key: key, Time: at(25), Value: 0, HasValue: true, Match: false})
		if d := e.Drain(); len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(25)) {
			t.Fatalf("a late non-match after the raise must resolve@25, got %+v", d)
		}
		if n := e.DrainLateSamples(); n != 0 {
			t.Fatalf("a late non-match is not a refused sample; counted %d", n)
		}
	})

	t.Run("count: a late match is refused and counted after its window closed", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: CountWindow, Count: 2, Agg: AggSum, Op: GT, Thresh: 1e9}}, 0)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 1, HasValue: true, Match: true})
		e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 1, HasValue: true, Match: true})
		e.Advance(at(21)) // the window {10, 20} closed
		e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(15), Value: 1, HasValue: true, Match: true})
		if n := e.DrainLateSamples(); n != 1 {
			t.Fatalf("late count %d, want 1", n)
		}
		if st := e.counts[key]; st == nil || st.count != 0 || !st.last.Equal(at(20)) {
			t.Fatalf("a refused sample must not be folded; state %+v", st)
		}
		// A non-match is simply ignored, late or not.
		e.ProcessEvent(Event{Seq: 4, Key: key, Time: at(15), Value: 1, HasValue: true, Match: false})
		if n := e.DrainLateSamples(); n != 0 {
			t.Fatalf("a non-match is not counted; counted %d", n)
		}
	})
}

// TestSampleIsFinalOnlyStrictlyPastTheFrontier pins the finality convention: a sample AT the
// frontier is still open — the same "a pane stays open while wm < end" rule — so with lateness 0
// a sample is evaluated on the next advance, stamped at its own time.
func TestSampleIsFinalOnlyStrictlyPastTheFrontier(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}, 0)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true})
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("the sample at the frontier is not final, got %+v", d)
	}
	if !e.HasPendingWork() {
		t.Fatal("a held sample is pending work: idle advance must keep moving the frontier for it")
	}
	if e.Advance(at(20)) { // the frontier does not move: nothing becomes final
		t.Fatal("an advance to the same frontier changed nothing, must report false")
	}
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("no frontier move, no verdict; got %+v", d)
	}
	if !e.Advance(at(20).Add(time.Nanosecond)) {
		t.Fatal("the frontier moved past the sample: must report a change")
	}
	if d := e.Drain(); len(d) != 1 || !d[0].At.Equal(at(20)) || d[0].Value != 100 {
		t.Fatalf("want raise@20 value 100, got %+v", d)
	}
	if e.HasPendingWork() {
		t.Fatal("nothing held and no timers: idle advance must go quiet again")
	}

	// The case that decides strictness: a frontier move that lands EXACTLY on a held sample. With a
	// lateness budget the sample is held before the frontier reaches it, and the move to its own
	// instant must leave it open — a same-instant sample can still arrive inside the budget.
	t.Run("a frontier move landing on the sample leaves it open", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}, 5*time.Second)
		e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(18), Value: 0, HasValue: true, Match: true})   // frontier 13
		e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true}) // frontier 15: both held
		e.Advance(at(25))                                                                              // frontier 20: @18 final, @20 exactly on it
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("the sample on the frontier is still open; got %+v", d)
		}
		if run := e.pending[key]; run == nil || len(run.samples) != 1 || !run.samples[0].t.Equal(at(20)) {
			t.Fatalf("want the sample at the frontier still held, got %+v", run)
		}
		e.Advance(at(25).Add(time.Nanosecond))
		if d := e.Drain(); len(d) != 1 || !d[0].At.Equal(at(20)) {
			t.Fatalf("one step past it, the sample is final: want raise@20, got %+v", d)
		}
	})
}

// TestSampleAtTheFrontierStaysOpenWhenAnEarlierOneFinalizes: finality is per SAMPLE, not per
// run. When a run holds one sample the frontier is past and one it is exactly on, only the first
// is applied; the second stays held, so a later-arriving sample at that same instant is still
// placed beside it by the tie order rather than refused or applied after it.
func TestSampleAtTheFrontierStaysOpenWhenAnEarlierOneFinalizes(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}}, 0)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true}) // frontier = 20: @10 final, @20 open
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("@10 only primes; @20 is on the frontier and must still be held, got %+v", d)
	}
	if run := e.pending[key]; run == nil || len(run.samples) != 1 || !run.samples[0].t.Equal(at(20)) {
		t.Fatalf("want exactly the sample at the frontier held, got %+v", run)
	}
	// Same instant, lower value: sorts BEFORE the held 100 by the tie order.
	e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(20), Value: 50, HasValue: true, Match: true})
	e.Advance(at(21))
	d := e.Drain()
	// In (time, value) order: 0@10 -> 50@20 (+50, raise) -> 100@20 (+50, still raised).
	if len(d) != 1 || d[0].Edge != EdgeRaised || d[0].Value != 50 {
		t.Fatalf("want one raise with delta 50 (the 50 placed before the 100), got %+v", d)
	}
}

// TestATieBetweenAMatchAndANonMatchResolvesFirst pins the documented tie convention: at one
// instant the falling edge is applied before the rising edge, so a device that reports a
// non-match and a match with the same timestamp ends RAISED, not cleared.
func TestATieBetweenAMatchAndANonMatchResolvesFirst(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}}, 0)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(20), Value: 0, HasValue: true, Match: false})
	e.Advance(at(21))
	d := e.Drain()
	if len(d) != 1 || d[0].Edge != EdgeRaised {
		t.Fatalf("non-match first, then the +100 raise: want to end raised with one edge, got %+v", d)
	}
	if _, raised := e.raised[key]; !raised {
		t.Fatal("the series must end raised")
	}
}

// TestRateModeSkipsAnEqualTimeSample: a second sample at the base's instant has no rate (dt = 0)
// and is skipped without moving the base — never divided through to an infinite rate that would
// clear any threshold.
func TestRateModeSkipsAnEqualTimeSample(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: DeltaRate, Rate: true, Op: GT, Thresh: 10}}, 0)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 0, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(10), Value: 100, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 3, Key: key, Time: at(20), Value: 100, HasValue: true, Match: true}) // 100/10 s = 10/s, not > 10
	e.Advance(at(21))
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("an equal-time sample has no rate and must be skipped; got %+v", d)
	}
	if base := e.lastVal[key]; base.value != 100 || !base.time.Equal(at(20)) {
		t.Fatalf("base must be the 20 s sample, got %+v", base)
	}
}

// TestCountWindowFrontierSurvivesRestore: the count state's `last` is what refuses a sample from
// a window already closed; a restore that forgot it would fold such a sample into the next window.
func TestCountWindowFrontierSurvivesRestore(t *testing.T) {
	rules := []Rule{{ID: "c", Kind: CountWindow, Count: 2, Agg: AggSum, Op: GT, Thresh: 1e9}}
	e := NewEngine(rules, 0)
	key := SeriesKey{Rule: "c", Series: "dev"}
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(10), Value: 1, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: key, Time: at(20), Value: 1, HasValue: true, Match: true})
	e.Advance(at(21)) // the window {10, 20} closed; last = 20
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Restore(rules, 0, snap)
	if err != nil {
		t.Fatal(err)
	}
	e2.ProcessEvent(Event{Seq: 3, Key: key, Time: at(15), Value: 1, HasValue: true, Match: true})
	if n := e2.DrainLateSamples(); n != 1 {
		t.Fatalf("a sample from the closed window must still be refused after a restore; late count %d", n)
	}
	if len(e2.pending) != 0 {
		t.Fatal("the refused sample must not be held")
	}
}

// TestPendingSamplesAreRetainedRecords: a held sample is memory that grows with the data, so the
// retained-sample gauge (the state budget's second axis) must see it, and the run itself is a
// live entry.
func TestPendingSamplesAreRetainedRecords(t *testing.T) {
	rules := []Rule{
		{ID: "acme/p@1/delta", Kind: DeltaRate, Op: GT, Thresh: 1e9},
		{ID: "acme/p@1/count", Kind: CountWindow, Count: 100, Agg: AggSum, Op: GT, Thresh: 1e9},
	}
	e := NewEngine(rules, 5*time.Second)
	for i := 1; i <= 3; i++ {
		for j, r := range rules {
			e.ProcessEvent(Event{Seq: uint64(i*10 + j), Key: SeriesKey{Rule: r.ID, Series: "d1"},
				Time: at(100 + i), Value: float64(i), HasValue: true, Match: true})
		}
	}
	retained, keys := e.RetainedSampleCounts(), e.LiveKeyCounts()
	for _, r := range rules {
		if retained[r.ID] != 3 {
			t.Fatalf("%s holds 3 samples inside the lateness budget, gauge says %d", r.ID, retained[r.ID])
		}
		if keys[r.ID] == 0 {
			t.Fatalf("%s: the open run is a live entry, got 0", r.ID)
		}
	}
	e.Advance(at(200))
	e.Drain()
	for _, r := range rules {
		if n := e.RetainedSampleCounts()[r.ID]; n != 0 {
			t.Fatalf("%s: every sample is final, gauge still says %d", r.ID, n)
		}
	}
}

// TestFlushOrderIsDeterministicAcrossSeries: a frontier move finalizes held samples across series
// in (time, key) order, whatever order the series were first seen in — so a replay emits the same
// sequence, not merely the same set.
func TestFlushOrderIsDeterministicAcrossSeries(t *testing.T) {
	x := Event{Key: SeriesKey{Rule: "r", Series: "x"}, Time: at(5), Value: 1, HasValue: true, Match: true}
	y := Event{Key: SeriesKey{Rule: "r", Series: "y"}, Time: at(3), Value: 1, HasValue: true, Match: true}
	run := func(order ...Event) []Detection {
		e := NewEngine([]Rule{{ID: "r", Kind: CountWindow, Count: 1, Agg: AggSum, Op: GT, Thresh: 0}}, 100*time.Second)
		for i, ev := range order {
			ev.Seq = uint64(i + 1)
			e.ProcessEvent(ev)
		}
		e.Advance(at(1000))
		return e.Drain()
	}
	a, b := run(x, y), run(y, x)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("flush order followed arrival\n x first: %+v\n y first: %+v", a, b)
	}
	if len(a) != 2 || a[0].Series != "y" || !a[0].At.Equal(at(3)) || a[1].Series != "x" {
		t.Fatalf("want the earlier sample's series (y@3) first, got %+v", a)
	}
}

// TestHeldSamplesSurviveRestartMidStream: the run is engine state, so a checkpoint taken while
// samples are held, restored and replayed from the next sequence, emits exactly what the
// uninterrupted engine emits and ends in the same state. This is the replay-correctness invariant
// for the new state; the sharded differential covers it under randomized crashes as well.
func TestHeldSamplesSurviveRestartMidStream(t *testing.T) {
	rules := []Rule{
		{ID: "d", Kind: DeltaRate, Op: GT, Thresh: 30},
		{ID: "c", Kind: CountWindow, Count: 3, Agg: AggAvg, Op: GE, Thresh: 50},
	}
	r := rand.New(rand.NewSource(42))
	var stream []Event
	tmax := 0
	for i := 1; i <= 120; i++ {
		tmax += r.Intn(4)
		tt := tmax - r.Intn(3) // mild reorder, inside the 10 s budget
		for _, rule := range rules {
			stream = append(stream, Event{Seq: uint64(i), Key: SeriesKey{Rule: rule.ID, Series: fmt.Sprint("dev", r.Intn(3))},
				Time: at(tt), Value: float64(r.Intn(100)), HasValue: true, Match: r.Intn(6) != 0})
		}
	}
	continuous := NewEngine(rules, 10*time.Second)
	var wantDets []Detection
	for i := 0; i < len(stream); i += 2 {
		continuous.ProcessResolved(stream[i].Seq, stream[i].Time, stream[i:i+2])
		wantDets = append(wantDets, continuous.Drain()...)
	}
	continuous.Advance(at(10_000))
	wantDets = append(wantDets, continuous.Drain()...)
	wantSnap, _ := continuous.Snapshot()

	restarted := NewEngine(rules, 10*time.Second)
	var gotDets []Detection
	for i := 0; i < len(stream); i += 2 {
		restarted.ProcessResolved(stream[i].Seq, stream[i].Time, stream[i:i+2])
		gotDets = append(gotDets, restarted.Drain()...)
		if (i/2)%7 == 3 { // crash and restore from the checkpoint, every few messages
			snap, err := restarted.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if restarted, err = Restore(rules, 10*time.Second, snap); err != nil {
				t.Fatal(err)
			}
			if len(restarted.pending) == 0 && i < len(stream)-10 {
				continue // nothing was held at this checkpoint; the next one may hold something
			}
		}
	}
	restarted.Advance(at(10_000))
	gotDets = append(gotDets, restarted.Drain()...)
	gotSnap, _ := restarted.Snapshot()
	if fmt.Sprint(gotDets) != fmt.Sprint(wantDets) {
		t.Fatalf("restart changed the detections\n continuous: %+v\n restarted: %+v", wantDets, gotDets)
	}
	if !bytes.Equal(gotSnap, wantSnap) {
		t.Fatalf("restart changed the final state\n continuous: %s\n restarted: %s", wantSnap, gotSnap)
	}
	if len(wantDets) < 10 {
		t.Fatalf("only %d detections: the stream barely exercises the kinds", len(wantDets))
	}
}

// TestPreReorderCheckpointRestores: a checkpoint written before the run existed has no "pending"
// section and no "last" on a count state. It restores as a model that held nothing back and refused
// nothing, which is exactly what that engine was.
func TestPreReorderCheckpointRestores(t *testing.T) {
	rules := []Rule{
		{ID: "d", Kind: DeltaRate, Op: GT, Thresh: 50},
		{ID: "c", Kind: CountWindow, Count: 3, Agg: AggSum, Op: GT, Thresh: 10},
	}
	old := `{"watermark":"2026-01-01T00:00:20Z","lastSeq":4,"active":[],"durationBreaks":[],"timers":[],"gens":[],` +
		`"sliding":[],"panes":[],"deltas":[{"rule":"d","series":"dev","value":100,"time":"2026-01-01T00:00:20Z"}],` +
		`"counts":[{"rule":"c","series":"dev","count":2,"sum":8,"min":3,"max":5}],"sessions":[],"slides":[],"corr":[],` +
		`"expected":[],"raised":[],"presence":[]}`
	e, err := Restore(rules, 0, []byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.pending) != 0 || e.pendHeap.Len() != 0 {
		t.Fatal("an old checkpoint holds nothing")
	}
	e.ProcessEvent(Event{Seq: 5, Key: SeriesKey{Rule: "d", Series: "dev"}, Time: at(30), Value: 200, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 6, Key: SeriesKey{Rule: "c", Series: "dev"}, Time: at(30), Value: 4, HasValue: true, Match: true})
	e.Advance(at(31))
	d := e.Drain()
	if len(d) != 2 {
		t.Fatalf("want the delta vs the restored base and the window closing on the restored count, got %+v", d)
	}
	for _, x := range d {
		switch x.RuleID {
		case "d":
			if x.Edge != EdgeRaised || x.Value != 100 {
				t.Fatalf("delta vs restored base 100 must be +100, got %+v", x)
			}
		case "c":
			if x.Edge != EdgeRaised || x.Value != 12 {
				t.Fatalf("window of restored 2 + 1 must sum 12, got %+v", x)
			}
		}
	}
}

// TestHeldSamplesAreSweptWithTheirKey: a descope, a rule removal and a tenant purge must release
// the run and its heap entry, or a flush fires for a series or rule that is gone.
func TestHeldSamplesAreSweptWithTheirKey(t *testing.T) {
	rules := []Rule{
		{ID: "acme/p@1/d", Kind: DeltaRate, Op: GT, Thresh: 0},
		{ID: "acme/p@1/c", Kind: CountWindow, Count: 1, Agg: AggSum, Op: GT, Thresh: 0},
	}
	feed := func(e *Engine) {
		seq := uint64(0)
		for _, r := range rules {
			for _, dev := range []string{"a", "b"} {
				seq++
				e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: r.ID, Series: dev},
					Time: at(100), Value: 1, HasValue: true, Match: true})
			}
		}
	}
	t.Run("descope", func(t *testing.T) {
		e := NewEngine(rules, time.Minute)
		feed(e)
		if len(e.pending) != 4 || e.pendHeap.Len() != 4 {
			t.Fatalf("setup: %d runs, heap %d", len(e.pending), e.pendHeap.Len())
		}
		if !e.Descope(rules[0].ID, "a", at(100)) {
			t.Fatal("descoping a series with a held sample must report a change")
		}
		if len(e.pending) != 3 || e.pendHeap.Len() != 3 {
			t.Fatalf("after descope: %d runs, heap %d", len(e.pending), e.pendHeap.Len())
		}
		e.Advance(at(1000))
		for _, d := range e.Drain() {
			if d.RuleID == rules[0].ID && d.Series == "a" {
				t.Fatalf("a descoped series fired from its held sample: %+v", d)
			}
		}
	})
	t.Run("remove rule", func(t *testing.T) {
		e := NewEngine(rules, time.Minute)
		feed(e)
		e.RemoveRule(rules[1].ID)
		if len(e.pending) != 2 || e.pendHeap.Len() != 2 {
			t.Fatalf("after removal: %d runs, heap %d", len(e.pending), e.pendHeap.Len())
		}
		for k := range e.pending {
			if k.Rule == rules[1].ID {
				t.Fatal("the removed rule's run survived")
			}
		}
	})
	t.Run("purge counts the runs", func(t *testing.T) {
		e := NewEngine(rules, time.Minute)
		feed(e)
		n := e.RemoveMatching(func(id string) bool { return true })
		// 2 rules + 4 runs; nothing else is live (no base yet, no timers, no latch).
		if n != 6 {
			t.Fatalf("purge removed %d entries, want 6 (2 rules + 4 held runs)", n)
		}
		if e.pendHeap.Len() != 0 || e.HasPendingWork() {
			t.Fatal("the purge left heap entries behind")
		}
	})
}
