// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

// thresholdRule is the one Threshold rule these tests drive.
var thresholdRule = Rule{ID: "r", Kind: Threshold}

// thrEv builds one Threshold reading for series s at second sec.
func thrEv(s string, sec int, match bool, v float64) Event {
	return Event{Key: SeriesKey{Rule: "r", Series: s}, Time: at(sec), Match: match, Value: v, HasValue: true}
}

// feedArrivals feeds evs to det in the given order, one message each, the message stamped at the
// reading's own time, and returns every detection.
func feedArrivals(det detector, evs []Event, order []int, seq0 uint64) []Detection {
	var out []Detection
	for i, idx := range order {
		ev := evs[idx]
		ev.Seq = seq0 + uint64(i) + 1
		det.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
		out = append(out, det.Drain()...)
	}
	return out
}

// newestDecides reports whether a series' readings at its newest instant agree on match. Only then
// is the event-time level after the burst defined independently of how a tie at that instant is
// broken: a match and a non-match at the same instant are both "the newest reading".
func newestDecides(evs []Event, s string) (level, decided bool) {
	var newest time.Time
	for _, e := range evs {
		if e.Key.Series == s && e.Time.After(newest) {
			newest = e.Time
		}
	}
	seen := false
	for _, e := range evs {
		if e.Key.Series != s || !e.Time.Equal(newest) {
			continue
		}
		if seen && e.Match != level {
			return false, false
		}
		seen, level = true, e.Match
	}
	return level, seen
}

// TestThresholdFinalLevelIsTheNewestReadings checks Threshold against the definition of a level:
// after a burst, a series is raised exactly when its newest reading matched. The readings arrive
// in a random order — event time plus a jitter wider than the lateness budget, so both in-budget
// reorders and readings behind the frontier occur — over several interleaved series, through a
// single engine and a sharded one.
//
// It also pins what may be emitted on the way: edges for a series alternate Raised/Resolved and
// are never stamped before the edge they follow, and a raise carries the value of the reading that
// raised it. On main, arrival order decided the level: a newer match was absorbed by the latch and
// an older non-match then cleared it, and an older match re-raised after a newer non-match.
func TestThresholdFinalLevelIsTheNewestReadings(t *testing.T) {
	series := []string{"a", "b", "c", "d"}
	for _, lateness := range []time.Duration{0, 3 * time.Second} {
		rng := rand.New(rand.NewSource(int64(4200 + lateness/time.Second)))
		decided, reordered, raisedFinal, lowFinal := 0, 0, 0, 0
		for trial := 0; trial < 600; trial++ {
			var evs []Event
			for _, s := range series {
				n := 3 + rng.Intn(12)
				for i := 0; i < n; i++ {
					evs = append(evs, thrEv(s, 1+rng.Intn(40), rng.Intn(100) < 55, float64(rng.Intn(100))))
				}
			}
			jit := make([]float64, len(evs))
			for i := range jit {
				jit[i] = float64(evs[i].Time.Unix()) + rng.Float64()*8
			}
			order := make([]int, len(evs))
			for i := range order {
				order[i] = i
			}
			sort.SliceStable(order, func(a, b int) bool { return jit[order[a]] < jit[order[b]] })
			for _, k := range []int{1, 3} {
				var det detector
				if k == 1 {
					det = NewEngine([]Rule{thresholdRule}, lateness)
				} else {
					det = NewSharded(k, []Rule{thresholdRule}, lateness)
				}
				got := perSeriesDetections(feedArrivals(det, evs, order, 0))
				for _, s := range series {
					ds := got[s]
					for i := range ds {
						if want := EdgeKind(i % 2); ds[i].Edge != want {
							t.Fatalf("trial %d K=%d series %s: edges do not alternate: %s", trial, k, s, fmtDets(ds))
						}
						if i > 0 && ds[i].At.Before(ds[i-1].At) {
							t.Fatalf("trial %d K=%d series %s: an edge is stamped before the one it follows: %s", trial, k, s, fmtDets(ds))
						}
						if ds[i].Edge == EdgeRaised && !raisedBy(evs, s, ds[i]) {
							t.Fatalf("trial %d K=%d series %s: %s is not a matching reading of the series", trial, k, s, fmtDet(ds[i]))
						}
					}
					want, ok := newestDecides(evs, s)
					if !ok {
						continue
					}
					if k == 1 {
						decided++
						if want {
							raisedFinal++
						} else {
							lowFinal++
						}
					}
					if gotLevel := len(ds) > 0 && ds[len(ds)-1].Edge == EdgeRaised; gotLevel != want {
						var sel []Event
						for _, idx := range order {
							if evs[idx].Key.Series == s {
								sel = append(sel, evs[idx])
							}
						}
						t.Fatalf("lateness %v trial %d K=%d series %s: final level raised=%v, but the newest reading says %v\n arrival: %s\n edges:   %s",
							lateness, trial, k, s, gotLevel, want, describeSamples(sel), fmtDets(ds))
					}
				}
			}
			if !sort.SliceIsSorted(order, func(a, b int) bool { return evs[order[a]].Time.Before(evs[order[b]].Time) }) {
				reordered++
			}
		}
		if decided < 1500 || reordered < 500 || raisedFinal < 500 || lowFinal < 500 {
			t.Fatalf("lateness %v: generator too weak: %d decided series (%d raised, %d low), %d reordered trials", lateness, decided, raisedFinal, lowFinal, reordered)
		}
	}
}

// edges is fmtDets without its trailing separator, for comparing against a literal.
func edges(ds []Detection) string { return strings.TrimSpace(fmtDets(ds)) }

// raisedBy reports whether a raise is stamped at, and carries the value of, a matching reading of
// the series.
func raisedBy(evs []Event, s string, d Detection) bool {
	for _, e := range evs {
		if e.Key.Series == s && e.Match && e.Time.Equal(d.At) && e.Value == d.Value {
			return true
		}
	}
	return false
}

// mainThreshold is the Threshold rule as it behaved before the cursor, written out directly: a
// match raises unless raised, a non-match resolves unless it is older than the raise. For readings
// that reach the engine in time order — ties included — the cursor must change nothing.
func mainThreshold(evs []Event) []Detection {
	raised := map[string]time.Time{}
	var out []Detection
	for _, e := range evs {
		s := e.Key.Series
		at, up := raised[s]
		if e.Match {
			if !up {
				raised[s] = e.Time
				out = append(out, Detection{RuleID: "r", Series: s, Kind: Threshold, At: e.Time, Value: e.Value, HasValue: e.HasValue})
			}
		} else if up && !e.Time.Before(at) {
			delete(raised, s)
			out = append(out, Detection{RuleID: "r", Series: s, Kind: Threshold, At: e.Time, Edge: EdgeResolved})
		}
	}
	return out
}

// TestThresholdInOrderReadingsAreUnchanged: readings that reach the engine in time order, per
// series, get exactly the edges and stamps they did before the cursor, and none is counted late.
// The streams include ties at one instant in either match order, a series far behind the frontier
// (a store-and-forward upload, in order, after a live device moved the frontier on), quiet spells
// longer than the cursor's horizon, and a series' readings arriving interleaved with others'.
func TestThresholdInOrderReadingsAreUnchanged(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	for trial := 0; trial < 400; trial++ {
		var evs []Event
		for si, s := range []string{"live", "a", "b", "behind"} {
			sec := 1
			if s == "behind" {
				sec = -3600 // buffered an hour behind what "live" carries the frontier to
			}
			n := 5 + rng.Intn(30)
			for i := 0; i < n; i++ {
				switch rng.Intn(6) {
				case 0: // a tie at the same instant
				case 1:
					sec += int(thresholdCursorHorizon/time.Second) + rng.Intn(60) // quiet past the horizon
				default:
					sec += 1 + rng.Intn(5)
				}
				evs = append(evs, thrEv(s, sec, rng.Intn(2) == 0, float64(si*1000+i)))
			}
		}
		// Interleave the series arbitrarily while keeping each one in time order.
		order := interleaveKeepingSeriesOrder(rng, evs)
		var in []Event
		for _, idx := range order {
			in = append(in, evs[idx])
		}
		want := mainThreshold(in)
		for _, k := range []int{1, 3} {
			var det detector
			if k == 1 {
				det = NewEngine([]Rule{thresholdRule}, 5*time.Second)
			} else {
				det = NewSharded(k, []Rule{thresholdRule}, 5*time.Second)
			}
			got := feedArrivals(det, evs, order, 0)
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
				t.Fatalf("trial %d K=%d: in-order readings changed\n want %s\n got  %s", trial, k, fmtDets(want), fmtDets(got))
			}
			if late := det.DrainLateSamples(); late != 0 {
				t.Fatalf("trial %d K=%d: %d in-order readings were counted late", trial, k, late)
			}
		}
	}
}

// interleaveKeepingSeriesOrder returns an arrival order over evs (built series by series, each in
// time order) that interleaves the series at random but keeps every series' own order.
func interleaveKeepingSeriesOrder(rng *rand.Rand, evs []Event) []int {
	queues := map[string][]int{}
	var names []string
	for i, e := range evs {
		if _, ok := queues[e.Key.Series]; !ok {
			names = append(names, e.Key.Series)
		}
		queues[e.Key.Series] = append(queues[e.Key.Series], i)
	}
	var order []int
	for len(order) < len(evs) {
		s := names[rng.Intn(len(names))]
		if q := queues[s]; len(q) > 0 {
			order = append(order, q[0])
			queues[s] = q[1:]
		}
	}
	return order
}

// TestThresholdRedeliveryBehindTheFrontierIsLate: a reading redelivered long after newer ones
// (an AckWait redelivery, 70 s here against a 5 s budget) neither clears nor raises, in either
// direction, and is counted late. On main it flipped the final level and was counted nowhere.
func TestThresholdRedeliveryBehindTheFrontierIsLate(t *testing.T) {
	for _, c := range []struct {
		name      string
		match     bool // the readings at 0, 20 and 80
		wantLevel bool
	}{
		{"an old non-match after newer matches", true, true},
		{"an old match after newer non-matches", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []int{1, 3} {
				var det detector
				if k == 1 {
					det = NewEngine([]Rule{thresholdRule}, 5*time.Second)
				} else {
					det = NewSharded(k, []Rule{thresholdRule}, 5*time.Second)
				}
				evs := []Event{thrEv("d", 0, c.match, 1), thrEv("d", 20, c.match, 2), thrEv("d", 80, c.match, 3), thrEv("d", 10, !c.match, 4)}
				var out []Detection
				for i, ev := range evs {
					ev.Seq = uint64(i + 1)
					arrival := ev.Time
					if i == 3 {
						arrival = at(80) // redelivered after the reading at 80 was applied
					}
					det.ProcessResolved(ev.Seq, arrival, []Event{ev})
					out = append(out, det.Drain()...)
				}
				if level := len(out) > 0 && out[len(out)-1].Edge == EdgeRaised; level != c.wantLevel {
					t.Fatalf("K=%d: final raised=%v, want %v (the newest reading); edges %s", k, level, c.wantLevel, fmtDets(out))
				}
				if c.match && (len(out) != 1 || !out[0].At.Equal(at(0))) {
					t.Fatalf("K=%d: want only Raised@0, got %s", k, fmtDets(out))
				}
				if !c.match && len(out) != 0 {
					t.Fatalf("K=%d: want no edges, got %s", k, fmtDets(out))
				}
				if late := det.DrainLateSamples(); late != 1 {
					t.Fatalf("K=%d: the redelivered reading must be counted late once, counted %d", k, late)
				}
			}
		})
	}
}

// TestThresholdStaleReadingInsideTheBudget pins the trade-off: a reading older than the series'
// newest is refused even inside the lateness budget, so a short blip delivered out of order is not
// reported — match@1, non-match@3, then match@2 stays low, where event-time order would have raised
// and cleared at 2 and 3. The reading is counted late. A reading AT the newest instant is not stale.
func TestThresholdStaleReadingInsideTheBudget(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs := []Event{thrEv("d", 1, true, 1), thrEv("d", 3, false, 0), thrEv("d", 2, true, 2), thrEv("d", 3, true, 3)}
	out := feedArrivals(e, evs, []int{0, 1, 2}, 0)
	if got := edges(out); got != "Raised@1=1 Resolved@3" {
		t.Fatalf("want Raised@1=1 Resolved@3 (the blip at 2 is lost), got %s", got)
	}
	if late := e.DrainLateSamples(); late != 1 {
		t.Fatalf("the stale reading at 2 must be counted late, counted %d", late)
	}
	out = feedArrivals(e, evs, []int{3}, 3)
	if got := edges(out); got != "Raised@3=3" {
		t.Fatalf("a reading at the newest instant is applied, not refused: want Raised@3=3, got %s", got)
	}
	if late := e.DrainLateSamples(); late != 0 {
		t.Fatalf("a reading at the newest instant is not late, counted %d", late)
	}
}

// TestThresholdCursorExpiresOncePastTheHorizon pins the bound on the cursor's memory and what it
// costs. A series that is not raised keeps its cursor while it is within the horizon of the
// frontier; once the frontier is further past it than that, the cursor is dropped (no live key, no
// checkpoint entry), and a reading older than it is applied as it was before the cursor existed. A
// raised series keeps its cursor however far behind it is, so an old reading never clears it.
func TestThresholdCursorExpiresOncePastTheHorizon(t *testing.T) {
	h := int(thresholdCursorHorizon / time.Second)
	rules := []Rule{thresholdRule}
	e := NewEngine(rules, 0)
	feed := func(seq uint64, s string, sec int, match bool, msgSec int) {
		ev := thrEv(s, sec, match, float64(sec))
		ev.Seq = seq
		e.ProcessResolved(seq, at(msgSec), []Event{ev})
	}
	feed(1, "low", 100, false, 100)
	feed(2, "up", 100, true, 100)
	if n := e.LiveKeyCounts()["r"]; n != 3 { // low's cursor, up's cursor and up's latch
		t.Fatalf("live keys = %d, want 3", n)
	}
	feed(3, "other", 99+h, false, 99+h) // the frontier is one second short of the horizon: still in force
	feed(4, "low", 50, true, 99+h)
	if d := e.Drain(); len(d) != 1 || d[0].Series != "up" {
		t.Fatalf("a reading older than a cursor at the horizon must be refused; got %s", fmtDets(d))
	}
	if late := e.DrainLateSamples(); late != 1 {
		t.Fatalf("late = %d, want 1", late)
	}
	// The frontier now sits exactly one horizon past low's cursor: it is expired, so low's old match
	// applies as it did before the cursor existed — and starts a new cursor at its own time, as an
	// absent one would, so a non-match after it (but still older than the expired cursor) clears it.
	// up is raised, so its cursor is in force however old, and its old non-match is still refused.
	feed(5, "other", 100+h, false, 100+h)
	feed(6, "low", 60, true, 100+h)
	feed(7, "up", 60, false, 100+h)
	feed(8, "low", 70, false, 100+h)
	if got := edges(e.Drain()); got != "Raised@60=60 Resolved@70" {
		t.Fatalf("want low's old match to raise at 60 and its non-match to clear at 70, and up's old non-match refused; got %s", got)
	}
	if late := e.DrainLateSamples(); late != 1 {
		t.Fatalf("late = %d, want 1 (up's refused reading)", late)
	}
	if n := e.LiveKeyCounts()["r"]; n != 3 { // other's cursor, up's cursor and up's latch; low's is gone
		t.Fatalf("live keys = %d, want 3 once low's cursor is past the horizon", n)
	}
	var s snapshot
	b, _ := e.Snapshot()
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.Newest {
		if x.Series == "low" {
			t.Fatalf("an expired cursor must not be checkpointed: %+v", s.Newest)
		}
	}
}

// TestThresholdCursorOutlastsRepeatedRedeliveries pins the horizon the docs promise: a device that
// is not in alarm and has gone quiet still has an old reading refused nine minutes of event time
// later — several AckWait redeliveries — while other devices carry the frontier on.
func TestThresholdCursorOutlastsRepeatedRedeliveries(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	feed := func(seq uint64, s string, sec int, match bool, msgSec int) {
		ev := thrEv(s, sec, match, 1)
		ev.Seq = seq
		e.ProcessResolved(seq, at(msgSec), []Event{ev})
	}
	feed(1, "quiet", 0, false, 0)
	feed(2, "busy", 9*60, false, 9*60)
	feed(3, "quiet", -1, true, 9*60)
	if d := e.Drain(); len(d) != 0 {
		t.Fatalf("an old match nine minutes after the newest reading must be refused; got %s", edges(d))
	}
	if late := e.DrainLateSamples(); late != 1 {
		t.Fatalf("late = %d, want 1", late)
	}
}

// TestThresholdCursorReplayIsCheckpointIndependent: the output is the same whether the engine runs
// straight through or is checkpointed and restored after every message — single and sharded — over
// reordered readings that span several cursor horizons. A cursor dropped at a checkpoint must be
// one whose absence changes nothing, or this differs.
func TestThresholdCursorReplayIsCheckpointIndependent(t *testing.T) {
	h := int(thresholdCursorHorizon / time.Second)
	rng := rand.New(rand.NewSource(99))
	rules := []Rule{thresholdRule}
	for trial := 0; trial < 150; trial++ {
		var evs []Event
		var arrivals []int
		sec := 0
		for i := 0; i < 60; i++ {
			s := []string{"a", "b", "c"}[rng.Intn(3)]
			if rng.Intn(8) == 0 {
				sec += h/2 + rng.Intn(h)
			} else {
				sec += rng.Intn(4)
			}
			t0 := sec
			if rng.Intn(4) == 0 {
				t0 -= rng.Intn(2 * h) // late, sometimes beyond the horizon
			}
			evs = append(evs, thrEv(s, t0, rng.Intn(2) == 0, float64(i)))
			arrivals = append(arrivals, sec)
		}
		run := func(k int, checkpoint bool) (string, uint64) {
			var det detector
			mk := func() detector {
				if k == 1 {
					return NewEngine(rules, 5*time.Second)
				}
				return NewSharded(k, rules, 5*time.Second)
			}
			det = mk()
			var out []Detection
			var late uint64
			for i, ev := range evs {
				ev.Seq = uint64(i + 1)
				det.ProcessResolved(ev.Seq, at(arrivals[i]), []Event{ev})
				out = append(out, det.Drain()...)
				late += det.DrainLateSamples()
				if checkpoint {
					b, err := det.(interface{ Snapshot() ([]byte, error) }).Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if k == 1 {
						det, err = Restore(rules, 5*time.Second, b)
					} else {
						det, err = RestoreSharded(k, rules, 5*time.Second, b)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			return fmtDets(out), late
		}
		for _, k := range []int{1, 3} {
			straight, l1 := run(k, false)
			restored, l2 := run(k, true)
			if straight != restored || l1 != l2 {
				t.Fatalf("trial %d K=%d: checkpointing changed the output\n straight (late %d): %s\n restored (late %d): %s", trial, k, l1, straight, l2, restored)
			}
		}
	}
}

// TestThresholdRestoresFromACheckpointWithoutCursors: a checkpoint written before the cursor
// existed carries the raise but no instant. The restore seeds the raised series' cursor at its
// rising edge — a lower bound — so an old non-match still cannot clear the alarm, and a series that
// was not raised starts with no cursor, as it did before.
func TestThresholdRestoresFromACheckpointWithoutCursors(t *testing.T) {
	rules := []Rule{thresholdRule}
	e := NewEngine(rules, 5*time.Second)
	feedArrivals(e, []Event{thrEv("up", 10, true, 1), thrEv("up", 20, true, 2), thrEv("low", 20, false, 0)}, []int{0, 1, 2}, 0)
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["newest"]) == "[]" || string(raw["newest"]) == "null" {
		t.Fatalf("the checkpoint should carry up's cursor at 20 and low's: %s", raw["newest"])
	}
	delete(raw, "newest")
	old, _ := json.Marshal(raw)
	r, err := Restore(rules, 5*time.Second, old)
	if err != nil {
		t.Fatal(err)
	}
	out := feedArrivals(r, []Event{thrEv("up", 5, false, 0), thrEv("up", 15, false, 0)}, []int{0, 1}, 3)
	if got := edges(out); got != "Resolved@15" {
		t.Fatalf("seeded at the raise (10): a non-match at 5 is refused, one at 15 is applied as before; got %s", got)
	}
	if late := r.DrainLateSamples(); late != 1 {
		t.Fatalf("late = %d, want 1", late)
	}
}

// TestThresholdCursorIsSweptWithTheSeries: a descope and a rule removal take the cursor with the
// rest of the series' state, so a reading after either is evaluated as the series' first.
func TestThresholdCursorIsSweptWithTheSeries(t *testing.T) {
	rules := []Rule{thresholdRule}
	e := NewEngine(rules, 5*time.Second)
	feedArrivals(e, []Event{thrEv("d", 20, false, 0)}, []int{0}, 0)
	e.Descope("r", "d", at(20))
	if got := edges(feedArrivals(e, []Event{thrEv("d", 19, true, 7)}, []int{0}, 1)); got != "Raised@19=7" {
		t.Fatalf("after a descope the old cursor must be gone; got %s", got)
	}
	e.RemoveRule("r")
	e.UpsertRule(thresholdRule)
	if got := edges(feedArrivals(e, []Event{thrEv("d", 18, true, 8)}, []int{0}, 2)); got != "Raised@18=8" {
		t.Fatalf("after a rule removal the old cursor must be gone; got %s", got)
	}
	if n := e.newest[SeriesKey{Rule: "r", Series: "d"}]; !n.Equal(at(18)) {
		t.Fatalf("cursor = %v, want the reading at 18", n)
	}
}
