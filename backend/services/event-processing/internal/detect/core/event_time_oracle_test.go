// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// TestSamplePairingKindsMatchAnEventTimeOracle checks the sample-pairing kinds against an
// INDEPENDENT reference rather than against themselves. TestSamplePairingKindsAreArrivalOrderIndependent
// compares the engine with itself under two orders, and its budget is wide enough that the frontier
// never passes a sample until the final advance — so it cannot see a flush that runs while some of a
// series' samples are final and others are still arriving, which is the case the pending run exists
// for, and it would pass an engine that is order-independent but wrong in BOTH orders.
//
// Here the budget is tight (3 s over samples spread across ~40 s), so frontier moves finalize part of
// a run while later samples are still in flight; the arrival order is a random permutation filtered
// to the ones the contract covers (every sample arrives before the frontier is past it); several
// series of one rule are interleaved; and both the single engine and a sharded one must emit, per
// series, exactly what a plain walk over the series' samples in event-time order says.
func TestSamplePairingKindsMatchAnEventTimeOracle(t *testing.T) {
	const lateness = 3 * time.Second
	kinds := []Rule{
		{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30},
		{ID: "r", Kind: DeltaRate, Rate: true, Op: GT, Thresh: 10},
		{ID: "r", Kind: DeltaRate, Op: LT, Thresh: -20},
		{ID: "r", Kind: CountWindow, Count: 3, Agg: AggSum, Op: GT, Thresh: 150},
		{ID: "r", Kind: CountWindow, Count: 2, Agg: AggMax, Op: LT, Thresh: 30},
	}
	series := []string{"a", "b", "c"}
	for ki, rule := range kinds {
		rng := rand.New(rand.NewSource(int64(1000 + ki)))
		raised, resolved, accepted := 0, 0, 0
		for trial := 0; trial < 400; trial++ {
			// Samples: per series, times on a coarse grid with ties, values and matches random.
			var evs []Event
			for _, s := range series {
				n := 3 + rng.Intn(10)
				for i := 0; i < n; i++ {
					evs = append(evs, Event{
						Key:  SeriesKey{Rule: "r", Series: s},
						Time: at(1 + rng.Intn(40)), Value: float64(rng.Intn(101)), HasValue: true,
						Match: rng.Intn(5) != 0,
					})
				}
			}
			// Arrival: event time plus a jitter wider than the budget, then keep the permutation only
			// if every sample arrives before the frontier is past it — the contract's own precondition.
			jit := make([]float64, len(evs))
			for i := range jit {
				jit[i] = float64(evs[i].Time.Unix()) + rng.Float64()*5
			}
			order := make([]int, len(evs))
			for i := range order {
				order[i] = i
			}
			sort.SliceStable(order, func(a, b int) bool { return jit[order[a]] < jit[order[b]] })
			if !inBudget(evs, order, lateness) {
				continue
			}
			accepted++

			want := map[string][]string{}
			for _, s := range series {
				for _, d := range oracleSeries(rule, evs, s) {
					want[s] = append(want[s], d)
					if d[0] == 'R' && d[1] == 'a' {
						raised++
					} else {
						resolved++
					}
				}
			}
			for _, k := range []int{1, 3} {
				var det detector
				if k == 1 {
					det = NewEngine([]Rule{rule}, lateness)
				} else {
					det = NewSharded(k, []Rule{rule}, lateness)
				}
				var out []Detection
				for i, idx := range order {
					ev := evs[idx]
					ev.Seq = uint64(i + 1)
					det.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
					out = append(out, det.Drain()...)
				}
				det.Advance(at(10_000))
				out = append(out, det.Drain()...)
				if late := det.DrainLateSamples(); late != 0 {
					t.Fatalf("rule %d trial %d K=%d: %d in-budget samples were refused as late", ki, trial, k, late)
				}
				got := map[string][]string{}
				for _, d := range out {
					got[d.Series] = append(got[d.Series], fmtDet(d))
				}
				for _, s := range series {
					if fmt.Sprint(got[s]) != fmt.Sprint(want[s]) {
						t.Fatalf("rule %d (%+v) trial %d K=%d series %s: engine disagrees with the event-time oracle\n samples: %s\n want: %v\n got:  %v",
							ki, rule, trial, k, s, describeSeries(evs, s), want[s], got[s])
					}
				}
			}
		}
		if accepted < 100 || raised < 50 || resolved < 30 {
			t.Fatalf("rule %d: generator too weak: %d accepted trials, %d raises, %d resolves", ki, accepted, raised, resolved)
		}
	}
}

// detector is the slice of the engine surface this test drives, satisfied by Engine and Sharded.
type detector interface {
	ProcessResolved(seq uint64, t time.Time, evs []Event)
	ProcessReceived(seq uint64, t time.Time, r Receipt, evs []Event)
	Advance(w time.Time) bool
	Drain() []Detection
	DrainLateSamples() uint64
}

// inBudget reports whether every sample in the arrival order arrives while the frontier (the
// newest time seen so far, including its own message, minus the lateness) is not past it.
func inBudget(evs []Event, order []int, lateness time.Duration) bool {
	var maxSeen time.Time
	for _, idx := range order {
		t := evs[idx].Time
		if t.After(maxSeen) {
			maxSeen = t
		}
		if t.Before(maxSeen.Add(-lateness)) {
			return false
		}
	}
	return true
}

func fmtDet(d Detection) string {
	if d.Edge == EdgeResolved {
		return fmt.Sprintf("Resolved@%d", int(d.At.Sub(base).Seconds()))
	}
	return fmt.Sprintf("Raised@%d=%g", int(d.At.Sub(base).Seconds()), d.Value)
}

func describeSeries(evs []Event, s string) string {
	var sel []Event
	for _, e := range evs {
		if e.Key.Series == s {
			sel = append(sel, e)
		}
	}
	return describeSamples(sel)
}

// oracleSeries walks one series' samples in event-time order — (time, non-match first, value) —
// and applies each kind's definition directly: DeltaRate pairs consecutive MATCHING samples (a
// rate skips an equal-time one), a non-match is a falling edge; CountWindow groups every Count
// consecutive matching samples. Both latch: a raise while raised is silent, a resolve needs a raise
// at or before it.
func oracleSeries(r Rule, evs []Event, s string) []string {
	var sel []Event
	for _, e := range evs {
		if e.Key.Series == s {
			sel = append(sel, e)
		}
	}
	sort.SliceStable(sel, func(i, j int) bool {
		a, b := sel[i], sel[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		if a.Match != b.Match {
			return !a.Match
		}
		return a.Value < b.Value
	})
	var out []string
	var raisedAt *time.Time
	raise := func(t time.Time, v float64) {
		if raisedAt != nil {
			return
		}
		tt := t
		raisedAt = &tt
		out = append(out, fmt.Sprintf("Raised@%d=%g", int(t.Sub(base).Seconds()), v))
	}
	clear := func(t time.Time) {
		if raisedAt == nil || t.Before(*raisedAt) {
			return
		}
		raisedAt = nil
		out = append(out, fmt.Sprintf("Resolved@%d", int(t.Sub(base).Seconds())))
	}
	switch r.Kind {
	case DeltaRate:
		var have bool
		var bt time.Time
		var bv float64
		for _, e := range sel {
			if !e.Match {
				clear(e.Time)
				continue
			}
			if have && r.Rate && !e.Time.After(bt) {
				continue
			}
			pt, pv, had := bt, bv, have
			bt, bv, have = e.Time, e.Value, true
			if !had {
				continue
			}
			q := e.Value - pv
			if r.Rate {
				q /= e.Time.Sub(pt).Seconds()
			}
			if cmp(r.Op, q, r.Thresh) {
				raise(e.Time, q)
			} else {
				clear(e.Time)
			}
		}
	case CountWindow:
		var vals []float64
		for _, e := range sel {
			if !e.Match {
				continue
			}
			vals = append(vals, e.Value)
			if len(vals) < r.Count {
				continue
			}
			var agg float64
			switch r.Agg {
			case AggSum:
				for _, v := range vals {
					agg += v
				}
			case AggMax:
				agg = vals[0]
				for _, v := range vals[1:] {
					if v > agg {
						agg = v
					}
				}
			default:
				panic("oracle: unsupported agg")
			}
			if cmp(r.Op, agg, r.Thresh) {
				raise(e.Time, agg)
			} else {
				clear(e.Time)
			}
			vals = vals[:0]
		}
	}
	return out
}

// TestRemovingARuleKeepsTheFlushHeapOrdered: purging one rule's runs compacts the heap array in
// place, and a compacted heap is not in general a heap — [A@1, B@9, B@2] is valid, [B@9, B@2] is
// not. A flush stops at the first run that is not due, so a broken heap hides a due run behind a
// later one and its verdict waits for an unrelated frontier move. The surviving rule's due sample
// must be settled by the very advance that passes it.
func TestRemovingARuleKeepsTheFlushHeapOrdered(t *testing.T) {
	rules := []Rule{
		{ID: "a", Kind: CountWindow, Count: 1, Agg: AggSum, Op: GT, Thresh: 0},
		{ID: "b", Kind: CountWindow, Count: 1, Agg: AggSum, Op: GT, Thresh: 0},
	}
	e := NewEngine(rules, 100*time.Second)
	// Pushed in this order the heap array is [a/x@1, b/y@9, b/z@2]: 2 is below 9's slot, not 1's.
	for i, s := range []struct {
		rule, series string
		sec          int
	}{{"a", "x", 1}, {"b", "y", 9}, {"b", "z", 2}} {
		ev := Event{Seq: uint64(i + 1), Key: SeriesKey{Rule: s.rule, Series: s.series}, Time: at(s.sec), Value: 10, HasValue: true, Match: true}
		e.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
	}
	e.RemoveMatching(func(id string) bool { return id == "a" })
	e.Advance(at(105)) // frontier 5: z@2 is due, y@9 is not
	d := e.Drain()
	if len(d) != 1 || d[0].RuleID != "b" || d[0].Series != "z" || !d[0].At.Equal(at(2)) {
		t.Fatalf("the advance past b/z@2 must settle it even after rule a's runs were purged; got %+v", d)
	}
}

// TestASampleAtTheFinalInstantIsStillPlaced pins the late boundary: late means OLDER than the
// series' newest final sample, so one AT its instant still has a position — a raw delta accepts
// same-time readings — and is applied, not refused and counted.
func TestASampleAtTheFinalInstantIsStillPlaced(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 50}}
	e := NewEngine(rules, 0)
	k := SeriesKey{Rule: "r", Series: "d"}
	feed := func(seq uint64, sec int, v float64) {
		e.ProcessResolved(seq, at(sec), []Event{{Seq: seq, Key: k, Time: at(sec), Value: v, HasValue: true, Match: true}})
	}
	feed(1, 10, 0)
	feed(2, 20, 0)   // finalizes 0@10: the base
	feed(3, 10, 100) // at the base's instant, behind the frontier: admitted, held
	e.Advance(at(21))
	d := e.Drain()
	if late := e.DrainLateSamples(); late != 0 {
		t.Fatalf("a sample at the final instant is not late; counted %d", late)
	}
	if len(d) != 2 || d[0].Edge != EdgeRaised || !d[0].At.Equal(at(10)) || d[0].Value != 100 ||
		d[1].Edge != EdgeResolved || !d[1].At.Equal(at(20)) {
		t.Fatalf("want Raised@10 (+100 against the same-instant base) then Resolved@20, got %+v", d)
	}
}
