// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
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

// promptReceipt is the receipt of a message stored on the stream the moment its reading was
// received; redeliveredReceipt is that of a message stored a full AckWait after the platform
// received its reading, as a redelivery is.
func promptReceipt(stored time.Time) Receipt { return Receipt{Received: stored, Stored: stored} }

func redeliveredReceipt(stored time.Time) Receipt {
	return Receipt{Received: stored.Add(-2 * thresholdRedeliveryBudget), Stored: stored}
}

// feedReceived is feedArrivals with a receipt per message: rs[i] goes with the i-th arrival.
func feedReceived(det detector, evs []Event, order []int, rs []Receipt, seq0 uint64) []Detection {
	var out []Detection
	for i, idx := range order {
		ev := evs[idx]
		ev.Seq = seq0 + uint64(i) + 1
		det.ProcessReceived(ev.Seq, ev.Time, rs[i], []Event{ev})
		out = append(out, det.Drain()...)
	}
	return out
}

// newDet builds a single engine (k == 1) or a sharded one.
func newDet(k int, lateness time.Duration) detector {
	if k == 1 {
		return NewEngine([]Rule{thresholdRule}, lateness)
	}
	return NewSharded(k, []Rule{thresholdRule}, lateness)
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

// refThreshold is the Threshold rule written out plainly, as a specification: readings in arrival
// order, each its own message stamped at its own time, against a frontier that trails the newest
// time seen by the lateness. A RAISED series refuses a reading older than its newest one, by up to
// the horizon; a reading older still is applied as it would be with no cursor and restarts the
// cursor (at the raise, if that is newer). An UNRAISED series holds nothing and refuses a match
// behind the frontier whose message may be a redelivery (redelivered[i] for the i-th reading; nil
// means every message may be). It returns the edges, the number of readings refused, and the number
// of matches behind the frontier that raised because their message reached the stream promptly.
func refThreshold(evs []Event, redelivered []bool, lateness time.Duration) ([]Detection, uint64, int) {
	type st struct{ raisedAt, cursor time.Time }
	state := map[string]*st{}
	var frontier time.Time
	first := true
	var out []Detection
	var late uint64
	prompt := 0
	for i, e := range evs {
		if f := e.Time.Add(-lateness); first || f.After(frontier) {
			frontier, first = f, false
		}
		s := e.Key.Series
		x, up := state[s]
		if !up {
			if !e.Match {
				continue
			}
			if e.Time.Before(frontier) {
				if redelivered == nil || redelivered[i] {
					late++
					continue
				}
				prompt++
			}
			state[s] = &st{raisedAt: e.Time, cursor: e.Time}
			out = append(out, Detection{RuleID: "r", Series: s, Kind: Threshold, At: e.Time, Value: e.Value, HasValue: e.HasValue})
			continue
		}
		if e.Time.Before(x.cursor) && e.Time.Add(thresholdCursorHorizon).After(x.cursor.Add(-time.Nanosecond)) {
			late++
			continue
		}
		cur := e.Time
		if x.raisedAt.After(cur) {
			cur = x.raisedAt
		}
		if !e.Match && !e.Time.Before(x.raisedAt) {
			delete(state, s)
			out = append(out, Detection{RuleID: "r", Series: s, Kind: Threshold, At: e.Time, Edge: EdgeResolved})
			continue
		}
		x.cursor = cur
	}
	return out, late, prompt
}

// TestThresholdUnderReorderMatchesItsSpecification drives several interleaved series through a
// random arrival order — event time plus a jitter wider than the lateness budget, so in-budget
// reorders and readings behind the frontier both occur — with a random receipt per message (stored
// promptly, stored as a redelivery is, or none), single and sharded, and checks the edges and the
// late count against refThreshold exactly. On top of that it checks the properties the
// specification exists for:
//
//   - edges alternate, a raise carries a matching reading of the series, and a resolve is never
//     stamped before the raise it clears;
//   - a series whose newest reading matched, and whose readings all arrived inside the budget,
//     ends raised — a newer match is never cleared by an older non-match;
//   - a series whose newest reading did not match ends clear, unless its last edge is a raise by an
//     older match that arrived inside the budget: the stale re-raise the rule accepts, exactly as
//     it behaved before the cursor, which the next reading heals.
func TestThresholdUnderReorderMatchesItsSpecification(t *testing.T) {
	series := []string{"a", "b", "c", "d"}
	promptTotal := 0 // only the 3 s budget is narrower than the jitter, so only it puts readings behind the frontier
	for _, lateness := range []time.Duration{3 * time.Second, 8 * time.Second} {
		rng := rand.New(rand.NewSource(int64(4200 + lateness/time.Second)))
		rngR := rand.New(rand.NewSource(int64(4300 + lateness/time.Second))) // receipts, apart so the readings are unchanged
		raisedChecked, clearChecked, staleReraise, refusedAny, promptAny := 0, 0, 0, 0, 0
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
			// Each message reaches the stream promptly, as a redelivery, or with no receipt.
			rs := make([]Receipt, len(order))
			redelivered := make([]bool, len(order))
			for i, idx := range order {
				switch rngR.Intn(5) {
				case 0, 1:
					rs[i] = promptReceipt(evs[idx].Time)
				case 2, 3:
					rs[i], redelivered[i] = redeliveredReceipt(evs[idx].Time), true
				default:
					redelivered[i] = true
				}
			}
			var arrived []Event
			inBudget := map[string]bool{}
			for _, s := range series {
				inBudget[s] = true
			}
			var frontier time.Time
			for i, idx := range order {
				ev := evs[idx]
				if f := ev.Time.Add(-lateness); i == 0 || f.After(frontier) {
					frontier = f
				}
				if ev.Time.Before(frontier) {
					inBudget[ev.Key.Series] = false
				}
				arrived = append(arrived, ev)
			}
			want, wantLate, prompt := refThreshold(arrived, redelivered, lateness)
			if wantLate > 0 {
				refusedAny++
			}
			if prompt > 0 {
				promptAny++
			}
			for _, k := range []int{1, 3} {
				det := newDet(k, lateness)
				all := feedReceived(det, evs, order, rs, 0)
				if late := det.DrainLateSamples(); fmtDets(all) != fmtDets(want) || late != wantLate {
					t.Fatalf("lateness %v trial %d K=%d: differs from the specification\n arrival: %s\n want (late %d): %s\n got  (late %d): %s",
						lateness, trial, k, describeSamples(arrived), wantLate, fmtDets(want), late, fmtDets(all))
				}
				got := perSeriesDetections(all)
				for _, s := range series {
					ds := got[s]
					for i := range ds {
						if wantEdge := EdgeKind(i % 2); ds[i].Edge != wantEdge {
							t.Fatalf("trial %d K=%d series %s: edges do not alternate: %s", trial, k, s, fmtDets(ds))
						}
						if ds[i].Edge == EdgeResolved && ds[i].At.Before(ds[i-1].At) {
							t.Fatalf("trial %d K=%d series %s: a resolve is stamped before its raise: %s", trial, k, s, fmtDets(ds))
						}
						if ds[i].Edge == EdgeRaised && !raisedBy(evs, s, ds[i]) {
							t.Fatalf("trial %d K=%d series %s: %s is not a matching reading of the series", trial, k, s, fmtDet(ds[i]))
						}
					}
					level, ok := newestDecides(evs, s)
					if !ok || k != 1 {
						continue
					}
					gotLevel := len(ds) > 0 && ds[len(ds)-1].Edge == EdgeRaised
					switch {
					case level && inBudget[s]:
						raisedChecked++
						if !gotLevel {
							t.Fatalf("trial %d series %s: the newest reading matched, in budget, but the series ended clear: %s", trial, s, fmtDets(ds))
						}
					case !level:
						clearChecked++
						if gotLevel {
							staleReraise++
							if len(ds) < 1 || !ds[len(ds)-1].At.Before(newestOf(evs, s)) {
								t.Fatalf("trial %d series %s: ended raised although the newest reading did not match, and not by an older match: %s", trial, s, fmtDets(ds))
							}
						}
					}
				}
			}
		}
		t.Logf("lateness %v: %d raised and %d clear series checked, %d stale re-raises, %d trials with a refusal, %d with a prompt raise behind the frontier",
			lateness, raisedChecked, clearChecked, staleReraise, refusedAny, promptAny)
		if raisedChecked < 200 || clearChecked < 500 || refusedAny < 100 || staleReraise == 0 {
			t.Fatalf("lateness %v: generator too weak: %d raised and %d clear series checked, %d stale re-raises, %d trials with a refusal",
				lateness, raisedChecked, clearChecked, staleReraise, refusedAny)
		}
		promptTotal += promptAny
	}
	if promptTotal < 100 {
		t.Fatalf("generator too weak: %d trials with a prompt raise behind the frontier", promptTotal)
	}
}

// newestOf is the newest reading time of series s.
func newestOf(evs []Event, s string) time.Time {
	var n time.Time
	for _, e := range evs {
		if e.Key.Series == s && e.Time.After(n) {
			n = e.Time
		}
	}
	return n
}

// mainThreshold is the Threshold rule as it behaved before the cursor, written out directly: a
// match raises unless raised, a non-match resolves unless it is older than the raise.
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

// TestThresholdInOrderReadingsAreUnchanged: a stream in time order — ties at one instant in either
// match order, quiet spells longer than the horizon, several series interleaved — gets exactly the
// edges and stamps it did before the cursor, and nothing is counted late.
func TestThresholdInOrderReadingsAreUnchanged(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	for trial := 0; trial < 400; trial++ {
		var evs []Event
		sec := 1
		n := 20 + rng.Intn(100)
		for i := 0; i < n; i++ {
			switch rng.Intn(8) {
			case 0: // a tie at the same instant
			case 1:
				sec += int(thresholdCursorHorizon/time.Second) + rng.Intn(60) // quiet past the horizon
			default:
				sec += 1 + rng.Intn(5)
			}
			s := []string{"a", "b", "c"}[rng.Intn(3)]
			evs = append(evs, thrEv(s, sec, rng.Intn(2) == 0, float64(i)))
		}
		order := make([]int, len(evs))
		for i := range order {
			order[i] = i
		}
		want := mainThreshold(evs)
		for _, k := range []int{1, 3} {
			det := newDet(k, 5*time.Second)
			got := feedArrivals(det, evs, order, 0)
			if fmtDets(got) != fmtDets(want) {
				t.Fatalf("trial %d K=%d: in-order readings changed\n want %s\n got  %s", trial, k, fmtDets(want), fmtDets(got))
			}
			if late := det.DrainLateSamples(); late != 0 {
				t.Fatalf("trial %d K=%d: %d in-order readings were counted late", trial, k, late)
			}
		}
	}
}

// TestThresholdReadingsBehindTheFrontierCanRaise: a device whose readings are dated more than the
// lateness budget behind the rest of the fleet — an in-order store-and-forward upload here, an hour
// behind a live device — raises from them when they reach the stream promptly, stamped at the
// reading, exactly as it did before the frontier bound. The same readings in messages that may be
// redeliveries — stored a full AckWait after they were received, or with no receipt — are refused
// and counted late, so the bound still holds against a redelivery. A raised alarm is cleared by
// such a reading either way: a raised series answers to its cursor, not to the frontier.
func TestThresholdReadingsBehindTheFrontierCanRaise(t *testing.T) {
	evs := []Event{
		thrEv("up", 0, true, 1),       // raised before the outage
		thrEv("live", 3600, false, 0), // a live device carries the frontier an hour on
		thrEv("up", 60, true, 2),      // the backlog, in order: a match...
		thrEv("low", 100, true, 3),    // ...another device's match, behind the frontier
		thrEv("up", 120, false, 0),    // ...and the raised device's non-match: it clears
		thrEv("low", 3600, true, 4),   // a match inside the budget
	}
	order := []int{0, 1, 2, 3, 4, 5}
	// Every message is stored at the wall time the backlog was uploaded, an hour on.
	wall := at(3600)
	for _, c := range []struct {
		name    string
		receipt Receipt
		want    string
		late    uint64
	}{
		{"stored promptly", promptReceipt(wall), "Raised@0=1 Raised@100=3 Resolved@120", 0},
		{"stored as a redelivery", redeliveredReceipt(wall), "Raised@0=1 Resolved@120 Raised@3600=4", 1},
		{"no receipt", Receipt{}, "Raised@0=1 Resolved@120 Raised@3600=4", 1},
		{"no receipt time", Receipt{Stored: wall}, "Raised@0=1 Resolved@120 Raised@3600=4", 1},
		{"no store time", Receipt{Received: wall}, "Raised@0=1 Resolved@120 Raised@3600=4", 1},
	} {
		for _, k := range []int{1, 3} {
			det := newDet(k, 5*time.Second)
			rs := make([]Receipt, len(order))
			for i := range rs {
				rs[i] = c.receipt
			}
			if got := edges(feedReceived(det, evs, order, rs, 0)); got != c.want {
				t.Fatalf("%s K=%d: want %s, got %s", c.name, k, c.want, got)
			}
			if late := det.DrainLateSamples(); late != c.late {
				t.Fatalf("%s K=%d: late = %d, want %d", c.name, k, late, c.late)
			}
		}
	}
}

// TestThresholdRedeliveryBudgetBoundary: a message stored exactly thresholdRedeliveryBudget after
// its reading was received is not a redelivery, and one stored a nanosecond later may be. The
// budget is half the AckWait, the least any redelivery waits, so it cannot reach one; a clock
// running behind on the ingest side (a receipt stamped after the store) is prompt.
func TestThresholdRedeliveryBudgetBoundary(t *testing.T) {
	if thresholdRedeliveryBudget != 30*time.Second {
		t.Fatalf("budget = %v, want half of a 60 s AckWait", thresholdRedeliveryBudget)
	}
	for _, c := range []struct {
		delay time.Duration
		raise bool
	}{
		{-time.Second, true},
		{0, true},
		{thresholdRedeliveryBudget, true},
		{thresholdRedeliveryBudget + time.Nanosecond, false},
		{2 * thresholdRedeliveryBudget, false},
	} {
		det := newDet(1, 5*time.Second)
		det.ProcessReceived(1, at(100), promptReceipt(at(100)), []Event{thrEv("live", 100, false, 0)})
		r := Receipt{Received: at(200), Stored: at(200).Add(c.delay)}
		det.ProcessReceived(2, at(10), r, []Event{thrEv("d", 10, true, 1)})
		got := edges(det.Drain())
		if raised := got == "Raised@10=1"; raised != c.raise || (!raised && got != "") {
			t.Fatalf("delay %v: want raise=%v, got %q", c.delay, c.raise, got)
		}
	}
}

// TestThresholdRedeliveryBehindTheFrontierIsLate: a reading redelivered long after newer ones
// (an AckWait redelivery, 70 s here against a 5 s budget: received at 10, stored again at 80)
// neither clears nor raises, in either direction, and is counted late. On main it flipped the
// final level and was counted nowhere.
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
				det := newDet(k, 5*time.Second)
				evs := []Event{thrEv("d", 0, c.match, 1), thrEv("d", 20, c.match, 2), thrEv("d", 80, c.match, 3), thrEv("d", 10, !c.match, 4)}
				var out []Detection
				for i, ev := range evs {
					ev.Seq = uint64(i + 1)
					arrival, r := ev.Time, promptReceipt(ev.Time)
					if i == 3 {
						// redelivered after the reading at 80 was applied
						arrival, r = at(80), Receipt{Received: at(10), Stored: at(80)}
					}
					det.ProcessReceived(ev.Seq, arrival, r, []Event{ev})
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

// TestThresholdRedeliveredMatchCannotReraiseAfterAClear is the case the frontier bound exists for:
// match@10 raises, non-match@80 clears, and then the message carrying match@10 is redelivered. It
// carries the receipt of its first delivery and is stored again an AckWait later, so it is refused
// and counted late rather than raising again stamped before the clear. The same reading reaching
// the platform afresh — a device sending it again — is a new reading as far as the engine can
// tell: it raises, stamped at 10, as every such reading did before the bound, and the device's next
// reading heals it. Telling those apart needs the series' own newest time, which an unraised series
// does not keep.
func TestThresholdRedeliveredMatchCannotReraiseAfterAClear(t *testing.T) {
	for _, c := range []struct {
		name    string
		receipt Receipt
		want    string
		late    uint64
	}{
		{"redelivered", Receipt{Received: at(10), Stored: at(80).Add(time.Minute)}, "Raised@10=1 Resolved@80", 1},
		{"sent again by the device", promptReceipt(at(80).Add(time.Minute)), "Raised@10=1 Resolved@80 Raised@10=1", 0},
	} {
		for _, k := range []int{1, 3} {
			det := newDet(k, 5*time.Second)
			det.ProcessReceived(1, at(10), promptReceipt(at(10)), []Event{thrEv("d", 10, true, 1)})
			det.ProcessReceived(2, at(80), promptReceipt(at(80)), []Event{thrEv("d", 80, false, 0)})
			det.ProcessReceived(3, at(10), c.receipt, []Event{thrEv("d", 10, true, 1)})
			if got := edges(det.Drain()); got != c.want {
				t.Fatalf("%s K=%d: want %s, got %s", c.name, k, c.want, got)
			}
			if late := det.DrainLateSamples(); late != c.late {
				t.Fatalf("%s K=%d: late = %d, want %d", c.name, k, late, c.late)
			}
			det.ProcessReceived(4, at(81), promptReceipt(at(81)), []Event{thrEv("d", 81, false, 0)})
			if got := edges(det.Drain()); c.late == 0 && got != "Resolved@81" {
				t.Fatalf("%s K=%d: the next reading must heal it, got %q", c.name, k, got)
			}
		}
	}
}

// TestThresholdStaleReadingInsideTheBudget pins both sides of the in-budget trade-off.
//
//   - RAISED: match@1, match@3, then non-match@2 is refused — it is older than the newest reading,
//     which matched — and counted late. Before the cursor it cleared the alarm.
//   - UNRAISED: match@1, non-match@3, then match@2 inside the budget raises at 2, stamped before the
//     clear at 3, exactly as before the cursor: an unraised series holds no cursor, and the reading
//     is not behind the frontier. The next reading heals it.
//
// A reading AT the cursor's instant is not stale.
func TestThresholdStaleReadingInsideTheBudget(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs := []Event{thrEv("d", 1, true, 1), thrEv("d", 3, true, 3), thrEv("d", 2, false, 0), thrEv("d", 3, false, 0)}
	if got := edges(feedArrivals(e, evs, []int{0, 1, 2}, 0)); got != "Raised@1=1" {
		t.Fatalf("raised: want the older non-match refused, Raised@1=1 only; got %s", got)
	}
	if late := e.DrainLateSamples(); late != 1 {
		t.Fatalf("raised: the stale non-match must be counted late, counted %d", late)
	}
	if got := edges(feedArrivals(e, evs, []int{3}, 3)); got != "Resolved@3" {
		t.Fatalf("a reading at the cursor's instant is applied: want Resolved@3, got %s", got)
	}

	e = NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs = []Event{thrEv("d", 1, true, 1), thrEv("d", 3, false, 0), thrEv("d", 2, true, 2), thrEv("d", 4, false, 0)}
	if got := edges(feedArrivals(e, evs, []int{0, 1, 2}, 0)); got != "Raised@1=1 Resolved@3 Raised@2=2" {
		t.Fatalf("unraised: want the in-budget stale re-raise as before the cursor, got %s", got)
	}
	if got := edges(mainThreshold([]Event{evs[0], evs[1], evs[2]})); got != "Raised@1=1 Resolved@3 Raised@2=2" {
		t.Fatalf("the reference for main disagrees: %s", got)
	}
	if late := e.DrainLateSamples(); late != 0 {
		t.Fatalf("unraised: nothing is late, counted %d", late)
	}
	if got := edges(feedArrivals(e, evs, []int{3}, 3)); got != "Resolved@4" {
		t.Fatalf("the next reading heals it: want Resolved@4, got %s", got)
	}
}

// TestThresholdHoldsStateOnlyForRaisedSeries pins the memory contract: keyed state and checkpoint
// bytes scale with RAISED alarms, not with active series. A fleet of series that report and never
// raise — and series that raised and cleared — leaves no live key and a checkpoint byte-identical
// to an engine that saw the same messages with no readings at all. A raised alarm holds its latch
// and, once a newer reading arrives, its cursor; clearing it drops both.
func TestThresholdHoldsStateOnlyForRaisedSeries(t *testing.T) {
	const n = 2000
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	bare := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	seq := uint64(0)
	feed := func(s string, sec int, match bool) {
		seq++
		ev := thrEv(s, sec, match, float64(sec))
		ev.Seq = seq
		e.ProcessResolved(seq, ev.Time, []Event{ev})
		bare.ProcessResolved(seq, ev.Time, nil)
		e.Drain()
	}
	snapEq := func(when string) {
		a, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		b, err := bare.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: the checkpoint is %d bytes, an engine with no readings %d", when, len(a), len(b))
		}
		if k := e.LiveKeyCounts()["r"]; k != 0 {
			t.Fatalf("%s: live keys = %d, want 0", when, k)
		}
	}
	for round := 0; round < 3; round++ {
		for i := 0; i < n; i++ {
			feed(fmt.Sprintf("dev-%d", i), 100*round+i%50, false)
		}
	}
	snapEq("a fleet that never raised")

	for i := 0; i < 10; i++ { // ten alarms; five see a newer reading
		feed(fmt.Sprintf("dev-%d", i), 400, true)
		if i%2 == 0 {
			feed(fmt.Sprintf("dev-%d", i), 401, true)
		}
	}
	if k := e.LiveKeyCounts()["r"]; k != 15 {
		t.Fatalf("live keys = %d, want 15: ten latches and five cursors", k)
	}
	b, _ := e.Snapshot()
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Newest) != 5 {
		t.Fatalf("checkpointed cursors = %d, want 5 (a cursor at its raise is re-derived): %+v", len(s.Newest), s.Newest)
	}
	for i := 0; i < 10; i++ {
		feed(fmt.Sprintf("dev-%d", i), 402, false)
	}
	snapEq("every alarm cleared")
}

// TestThresholdRaisedCursorHasNoFrontierHorizon: a raised device that has gone quiet refuses an
// older non-match however far the rest of the fleet has carried the frontier, and an unraised one
// refuses an older match because it is behind the frontier. Both are counted late.
func TestThresholdRaisedCursorHasNoFrontierHorizon(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	feed := func(seq uint64, s string, sec int, match bool, msgSec int) {
		ev := thrEv(s, sec, match, 1)
		ev.Seq = seq
		e.ProcessResolved(seq, at(msgSec), []Event{ev})
	}
	feed(1, "quiet", 0, false, 0)
	feed(2, "up", 0, true, 0)
	feed(3, "up", 300, true, 300)
	feed(4, "busy", 6*3600, false, 6*3600)
	feed(5, "quiet", -1, true, 6*3600)
	feed(6, "up", 200, false, 6*3600)
	if d := edges(e.Drain()); d != "Raised@0=1" {
		t.Fatalf("want only up's raise; got %s", d)
	}
	if late := e.DrainLateSamples(); late != 2 {
		t.Fatalf("late = %d, want 2", late)
	}
}

// TestThresholdCursorReplayIsCheckpointIndependent: the output is the same whether the engine runs
// straight through or is checkpointed and restored after every message — single and sharded — over
// reordered readings that span several cursor horizons, each message stored promptly, as a
// redelivery, or with no receipt. A cursor dropped at a checkpoint must be one whose absence changes
// nothing, and the receipt must be read from the message rather than from anything a checkpoint
// carries, or this differs.
func TestThresholdCursorReplayIsCheckpointIndependent(t *testing.T) {
	h := int(thresholdCursorHorizon / time.Second)
	rng := rand.New(rand.NewSource(99))
	rules := []Rule{thresholdRule}
	for trial := 0; trial < 150; trial++ {
		var evs []Event
		var arrivals []int
		var receipts []Receipt
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
			switch rng.Intn(3) {
			case 0:
				receipts = append(receipts, promptReceipt(at(sec)))
			case 1:
				receipts = append(receipts, redeliveredReceipt(at(sec)))
			default:
				receipts = append(receipts, Receipt{})
			}
		}
		run := func(k int, checkpoint bool) (string, uint64) {
			det := newDet(k, 5*time.Second)
			var out []Detection
			var late uint64
			for i, ev := range evs {
				ev.Seq = uint64(i + 1)
				det.ProcessReceived(ev.Seq, at(arrivals[i]), receipts[i], []Event{ev})
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
		t.Fatalf("the checkpoint should carry up's cursor at 20: %s", raw["newest"])
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

// TestThresholdCursorIsSweptWithTheSeries: a descope and a rule removal take a raised series'
// cursor with the rest of its state.
func TestThresholdCursorIsSweptWithTheSeries(t *testing.T) {
	rules := []Rule{thresholdRule}
	key := SeriesKey{Rule: "r", Series: "d"}
	e := NewEngine(rules, 5*time.Second)
	feedArrivals(e, []Event{thrEv("d", 20, true, 1), thrEv("d", 25, true, 2)}, []int{0, 1}, 0)
	if _, ok := e.newest[key]; !ok {
		t.Fatal("a raised series with a newer reading must hold a cursor")
	}
	e.Descope("r", "d", at(25))
	if _, ok := e.newest[key]; ok {
		t.Fatal("a descope must take the cursor")
	}
	if got := edges(feedArrivals(e, []Event{thrEv("d", 21, true, 7)}, []int{0}, 2)); got != "Resolved@25 Raised@21=7" {
		t.Fatalf("after a descope a match inside the budget raises afresh; got %s", got)
	}
	feedArrivals(e, []Event{thrEv("d", 26, true, 8)}, []int{0}, 3)
	e.RemoveRule("r")
	if _, ok := e.newest[key]; ok {
		t.Fatal("a rule removal must take the cursor")
	}
}

// TestThresholdClockStepBackIsBoundedByTheHorizon: a device whose clock steps BACK (an NTP
// correction, an RTC reset after power loss, a timezone bug) presents every later reading as older
// than its cursor.
//
//   - RAISED, a step back by more than the horizon: not a reorder. The reading is applied as it
//     would be with no cursor and restarts it. Without the bound, an alarm that kept matching for
//     hours refused every reading until the clock caught up with the old newest one.
//   - RAISED, a step no longer than the horizon: refused until the clock passes the old newest
//     reading, a stall no longer than the step. The boundary is exactly one horizon.
//   - UNRAISED: there is no cursor, and the stepped readings are behind the frontier, so a match is
//     refused until the clock reaches it — the accepted cost of the frontier bound.
func TestThresholdClockStepBackIsBoundedByTheHorizon(t *testing.T) {
	h := int(thresholdCursorHorizon / time.Second)
	for _, k := range []int{1, 3} {
		feed := func(det detector, seq *uint64, sec int, match bool) []Detection {
			*seq++
			ev := thrEv("d", sec, match, float64(sec))
			ev.Seq = *seq
			det.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
			return det.Drain()
		}

		// Raised at 0 and matching every minute for five hours; the clock then steps back an hour.
		det, seq := newDet(k, 5*time.Second), uint64(0)
		var out []Detection
		for sec := 0; sec <= 5*3600; sec += 60 {
			out = append(out, feed(det, &seq, sec, true)...)
		}
		out = append(out, feed(det, &seq, 4*3600+60, false)...)
		if got := edges(out); got != "Raised@0=0 Resolved@14460" {
			t.Fatalf("K=%d raised, stepped back 1h: want Raised@0=0 Resolved@14460 (as before the cursor), got %s", k, got)
		}

		// Raised at 1000; a step back shorter than the horizon is refused until the clock passes it.
		det, seq, out = newDet(k, 5*time.Second), 0, nil
		out = append(out, feed(det, &seq, 1000, true)...)
		step := h / 2
		for sec := 1000 - step + 60; sec <= 1000; sec += 60 {
			out = append(out, feed(det, &seq, sec, false)...)
		}
		if got := edges(out); got != "Raised@1000=1000 Resolved@1000" {
			t.Fatalf("K=%d raised, stepped back %ds: want the stall to end at the old newest reading, got %s", k, step, got)
		}
		if late, want := det.DrainLateSamples(), uint64(step/60-1); late != want {
			t.Fatalf("K=%d: late = %d, want %d (every stepped reading before the old newest one)", k, late, want)
		}

		// The boundary, on a raised series with a cursor ahead of its raise: a reading exactly one
		// horizon older than the cursor is refused as late; one a second older is applied (and,
		// being older than the raise, cannot clear it).
		det, seq, out = newDet(k, 5*time.Second), 0, nil
		out = append(out, feed(det, &seq, 50, true)...)
		out = append(out, feed(det, &seq, h+100, true)...)
		out = append(out, feed(det, &seq, 100, false)...)
		if late := det.DrainLateSamples(); late != 1 {
			t.Fatalf("K=%d: a reading exactly one horizon behind the cursor must be refused as late, counted %d", k, late)
		}
		out = append(out, feed(det, &seq, 99, false)...)
		if late := det.DrainLateSamples(); late != 0 {
			t.Fatalf("K=%d: a reading more than one horizon behind the cursor is applied, not late; counted %d", k, late)
		}
		if got := edges(out); got != "Raised@50=50 Resolved@99" {
			t.Fatalf("K=%d: want Raised@50=50 Resolved@99, got %s", k, got)
		}

		// Unraised, on an otherwise quiet engine; the clock steps back a day. The matches are
		// behind the frontier and refused until the clock reaches it.
		det, seq, out = newDet(k, 5*time.Second), 0, nil
		for sec := 0; sec <= 3600; sec += 60 {
			out = append(out, feed(det, &seq, sec, false)...)
		}
		out = append(out, feed(det, &seq, 3600-86400+60, true)...)
		out = append(out, feed(det, &seq, 3600, true)...)
		if got := edges(out); got != "Raised@3600=3600" {
			t.Fatalf("K=%d unraised, stepped back 1d: want the stepped match refused and the one at the frontier raised, got %s", k, got)
		}
		if late := det.DrainLateSamples(); late != 1 {
			t.Fatalf("K=%d: the stepped match is late, counted %d", k, late)
		}
	}
}

// TestThresholdBatchSamplesAreNotLateAgainstTheirOwnEnvelope: a message batching several readings
// moves the frontier to its envelope before its samples are applied. An unraised series measures a
// match against the frontier as it stood BEFORE that message, so a device sending ten one-second
// readings per message still raises from the oldest of them; a batch dated behind the frontier
// that earlier messages set is refused when its message may be a redelivery, and raises from its
// oldest match when the message reached the stream promptly.
func TestThresholdBatchSamplesAreNotLateAgainstTheirOwnEnvelope(t *testing.T) {
	for _, k := range []int{1, 3} {
		det := newDet(k, 5*time.Second)
		batch := []Event{thrEv("d", 1, true, 1), thrEv("d", 2, false, 0)}
		det.ProcessResolved(1, at(10), batch)
		if got := edges(det.Drain()); got != "Raised@1=1 Resolved@2" {
			t.Fatalf("K=%d: a batch's own envelope must not make its samples late; got %s", k, got)
		}
		det.ProcessResolved(2, at(100), []Event{thrEv("e", 100, false, 0)})
		det.ProcessResolved(3, at(100), []Event{thrEv("d", 50, true, 2), thrEv("d", 51, true, 3)})
		if got := edges(det.Drain()); got != "" {
			t.Fatalf("K=%d: a batch behind the frontier earlier messages set must be refused; got %s", k, got)
		}
		if late := det.DrainLateSamples(); late != 2 {
			t.Fatalf("K=%d: late = %d, want 2", k, late)
		}
		det.ProcessReceived(4, at(100), promptReceipt(at(100)), []Event{thrEv("d", 52, false, 0), thrEv("d", 53, true, 4), thrEv("d", 54, true, 5)})
		if got := edges(det.Drain()); got != "Raised@53=4" {
			t.Fatalf("K=%d: a prompt batch behind the frontier must raise from its oldest match; got %s", k, got)
		}
		if late := det.DrainLateSamples(); late != 0 {
			t.Fatalf("K=%d: late = %d, want 0", k, late)
		}
	}
}

// TestThresholdCursorDoesNotOutliveTheAlarm pins where the cursor lives, directly in the engine's
// map rather than through a snapshot or a live-key count (both of which prune first, and would hide
// a cursor left behind until then): it is stored only once a reading past the raise arrives, never
// for one AT the raise, and it goes when the alarm clears — by a reading, or by ClearRaised when the
// runtime drops the raise.
func TestThresholdCursorDoesNotOutliveTheAlarm(t *testing.T) {
	key := SeriesKey{Rule: "r", Series: "d"}
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	has := func() bool { _, ok := e.newest[key]; return ok }
	feedArrivals(e, []Event{thrEv("d", 10, true, 1), thrEv("d", 10, true, 2)}, []int{0, 1}, 0)
	if has() {
		t.Fatal("a reading at the raise must not store a cursor")
	}
	feedArrivals(e, []Event{thrEv("d", 11, true, 3)}, []int{0}, 2)
	if !has() {
		t.Fatal("a reading past the raise must store a cursor")
	}
	feedArrivals(e, []Event{thrEv("d", 12, false, 0)}, []int{0}, 3)
	if has() {
		t.Fatal("the clear must drop the cursor")
	}

	feedArrivals(e, []Event{thrEv("d", 20, true, 4), thrEv("d", 21, true, 5)}, []int{0, 1}, 4)
	e.Drain()
	e.ClearRaised(key)
	if n := e.LiveKeyCounts()["r"]; n != 0 {
		t.Fatalf("after ClearRaised the series holds %d live keys, want 0", n)
	}
	feedArrivals(e, []Event{thrEv("d", 30, true, 6), thrEv("d", 31, true, 7)}, []int{0, 1}, 6)
	e.ClearRaised(key)
	feedArrivals(e, []Event{thrEv("d", 32, false, 0)}, []int{0}, 8)
	if has() {
		t.Fatal("a reading on an unraised series must drop a cursor ClearRaised left behind")
	}
}

// TestThresholdProcessEventUsesTheFrontierBeforeTheEvent: the single-event entry point measures an
// unraised match against the frontier as it stood before that event, as ProcessResolved does.
func TestThresholdProcessEventUsesTheFrontierBeforeTheEvent(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	ev := thrEv("x", 100, false, 0)
	ev.Seq = 1
	e.ProcessEvent(ev)
	ev = thrEv("d", 50, true, 1)
	ev.Seq = 2
	e.ProcessEvent(ev)
	ev = thrEv("d", 99, true, 2)
	ev.Seq = 3
	e.ProcessEvent(ev)
	if got := edges(e.Drain()); got != "Raised@99=2" {
		t.Fatalf("want the match behind the frontier refused and the one inside it raised; got %s", got)
	}
}

// TestThresholdStepBehindTheRaiseCannotClearIt: a reading past the horizon behind the cursor is
// applied as it would be with no cursor — and with no cursor, the resolve guard still refuses a
// non-match older than the raise. A later one at or after the raise clears it.
func TestThresholdStepBehindTheRaiseCannotClearIt(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	var evs []Event
	for sec := 3600; sec <= 7200; sec += 60 {
		evs = append(evs, thrEv("d", sec, true, float64(sec)))
	}
	evs = append(evs, thrEv("d", 0, false, 0), thrEv("d", 3700, false, 0))
	order := make([]int, len(evs))
	for i := range order {
		order[i] = i
	}
	if got := edges(feedArrivals(e, evs, order, 0)); got != "Raised@3600=3600 Resolved@3700" {
		t.Fatalf("want Raised@3600=3600 Resolved@3700, got %s", got)
	}
}
