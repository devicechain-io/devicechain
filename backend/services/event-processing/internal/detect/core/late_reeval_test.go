// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// The trailing-window kinds (Repeating, SlidingAgg, Correlation) are not held back the way the
// sample-pairing kinds are: they answer on arrival. What makes that safe under bounded reordering
// is that a sample older than its series' newest event is evaluated AT that newest event — the
// window it is folded into is the one open at the series' newest instant, and any edge it causes
// is stamped there. These tests pin the consequence: the final level is the one an event-time walk
// reaches, and no edge is ever stamped before the edge it follows.

// windowOracleLevel walks one series' events in event-time order — (time, non-match first, value,
// member) — applying each kind's definition directly, and returns the final level plus whether the
// level is DECIDABLE from what the engine is entitled to keep (see the SlidingAgg note below).
//
//   - Repeating: raised iff the matching samples in (T−W, T] number at least Count, T being the
//     series' newest event. The kind's level is a pure function of that count, so it is always
//     decidable.
//   - Correlation: raised iff the distinct members with a matching sighting in (T−W, T] number at
//     least Count. Also a pure function of the window.
//   - SlidingAgg: a satisfied window raises only on a MATCHING sample — a non-match only ever ages
//     the window toward its falling edge (applySlidingAgg's gate). So the level after a trailing
//     non-match whose window satisfies is whatever it was before, which depends on the window at
//     earlier instants: samples older than T−W the engine has, by design, already let go. The
//     level is decidable when the window at T does not satisfy (low), or when the newest event is
//     a match (raised iff it satisfies). A match-every rule is therefore always decidable.
func windowOracleLevel(r Rule, evs []Event, s string) (raised, decidable bool) {
	var sel []Event
	for _, e := range evs {
		if e.Key.Series == s && (r.Kind != Correlation || e.Member != "") {
			sel = append(sel, e)
		}
	}
	sort.SliceStable(sel, func(i, j int) bool { return eventTimeLess(sel[i], sel[j]) })
	if len(sel) == 0 {
		return false, true
	}
	switch r.Kind {
	case Repeating, Correlation:
		T := sel[len(sel)-1].Time
		cut := T.Add(-r.Window)
		n := 0
		seen := map[string]bool{}
		for _, e := range sel {
			if !e.Match || !e.Time.After(cut) {
				continue
			}
			if r.Kind == Correlation {
				if !seen[e.Member] {
					seen[e.Member] = true
					n++
				}
				continue
			}
			n++
		}
		return n >= r.Count, true
	case SlidingAgg:
		// The engine's own in-order recursion, written out: L_i = S_i ? (m_i ? 1 : L_{i-1}) : 0.
		level := false
		var sats bool
		for i, e := range sel {
			cut := e.Time.Add(-r.Window)
			var w paneAgg
			for _, x := range sel[:i+1] {
				if x.Match && x.Time.After(cut) {
					w.add(x.Value)
				}
			}
			sats = w.count > 0 && cmp(r.Op, w.value(r.Agg), r.Thresh)
			switch {
			case !sats:
				level = false
			case e.Match:
				level = true
			}
		}
		last := sel[len(sel)-1]
		return level, !sats || last.Match
	}
	panic("windowOracleLevel: unsupported kind")
}

// eventTimeLess is the event-time order the oracle walks: time, non-match first, value, member,
// series — a total order over the contents, so equal-time samples have one defined position.
func eventTimeLess(a, b Event) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.Before(b.Time)
	}
	if a.Match != b.Match {
		return !a.Match
	}
	if a.Value != b.Value {
		return a.Value < b.Value
	}
	if a.Member != b.Member {
		return a.Member < b.Member
	}
	return a.Key.Series < b.Key.Series
}

type windowOracleCase struct {
	name     string
	rule     Rule
	lateness time.Duration
	jitter   float64 // seconds of arrival jitter; wider than the lateness, filtered to in-budget
	members  int     // Correlation only
	matchPct int
}

func windowOracleCases() []windowOracleCase {
	return []windowOracleCase{
		{name: "Repeating N3/W5", rule: Rule{ID: "r", Kind: Repeating, Count: 3, Window: 5 * time.Second}, lateness: 3 * time.Second, jitter: 5, matchPct: 60},
		{name: "Repeating N2/W3 (window < lateness)", rule: Rule{ID: "r", Kind: Repeating, Count: 2, Window: 3 * time.Second}, lateness: 5 * time.Second, jitter: 8, matchPct: 50},
		{name: "Correlation N3/W5", rule: Rule{ID: "r", Kind: Correlation, Count: 3, Window: 5 * time.Second}, lateness: 3 * time.Second, jitter: 5, members: 5, matchPct: 80},
		{name: "Correlation N2/W3 (window < lateness)", rule: Rule{ID: "r", Kind: Correlation, Count: 2, Window: 3 * time.Second}, lateness: 5 * time.Second, jitter: 8, members: 3, matchPct: 50},
		{name: "SlidingAgg avg>50 match-every", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50}, lateness: 3 * time.Second, jitter: 5, matchPct: 100},
		{name: "SlidingAgg min>30 match-every", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMin, Op: GT, Thresh: 30}, lateness: 3 * time.Second, jitter: 5, matchPct: 100},
		{name: "SlidingAgg avg>50", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50}, lateness: 3 * time.Second, jitter: 5, matchPct: 90},
		{name: "SlidingAgg avg<40", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: LT, Thresh: 40}, lateness: 3 * time.Second, jitter: 5, matchPct: 90},
		{name: "SlidingAgg min>30", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMin, Op: GT, Thresh: 30}, lateness: 3 * time.Second, jitter: 5, matchPct: 90},
		{name: "SlidingAgg max<60", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMax, Op: LT, Thresh: 60}, lateness: 3 * time.Second, jitter: 5, matchPct: 90},
		{name: "SlidingAgg count<3", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggCount, Op: LT, Thresh: 3}, lateness: 3 * time.Second, jitter: 5, matchPct: 70},
		{name: "SlidingAgg avg>50 W30", rule: Rule{ID: "r", Kind: SlidingAgg, Window: 30 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50}, lateness: 5 * time.Second, jitter: 8, matchPct: 90},
	}
}

// genWindowTrial draws one series set and an in-budget arrival order that differs from event-time
// order, or reports false when the draw is not one.
func genWindowTrial(rng *rand.Rand, c windowOracleCase, series []string) ([]Event, []int, bool) {
	var evs []Event
	for _, s := range series {
		n := 3 + rng.Intn(12)
		for i := 0; i < n; i++ {
			ev := Event{
				Key:  SeriesKey{Rule: "r", Series: s},
				Time: at(1 + rng.Intn(40)), Value: float64(rng.Intn(101)), HasValue: true,
				Match: rng.Intn(100) < c.matchPct,
			}
			if c.members > 0 {
				ev.Member = fmt.Sprintf("m%d", rng.Intn(c.members))
			}
			evs = append(evs, ev)
		}
	}
	jit := make([]float64, len(evs))
	for i := range jit {
		jit[i] = float64(evs[i].Time.Unix()) + rng.Float64()*c.jitter
	}
	order := make([]int, len(evs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return jit[order[a]] < jit[order[b]] })
	if !inBudget(evs, order, c.lateness) {
		return nil, nil, false
	}
	sorted := append([]int(nil), order...)
	sort.SliceStable(sorted, func(a, b int) bool { return eventTimeLess(evs[sorted[a]], evs[sorted[b]]) })
	for i := range sorted {
		if sorted[i] != order[i] {
			return evs, order, true
		}
	}
	return nil, nil, false // arrived in event-time order: not a reordering
}

func feedWindowTrial(det detector, evs []Event, order []int) []Detection {
	var out []Detection
	for i, idx := range order {
		ev := evs[idx]
		ev.Seq = uint64(i + 1)
		det.ProcessResolved(ev.Seq, ev.Time, []Event{ev})
		out = append(out, det.Drain()...)
	}
	return out
}

// TestTrailingWindowKindsReachTheEventTimeLevel is the oracle for the trailing-window kinds under
// in-budget reordering: random series, random arrival permutations filtered to the ones the
// lateness contract covers, a single engine and a 3-way sharded one. For every series the final
// level must be the one the event-time walk reaches (where that level is decidable — always, for
// Repeating, Correlation and a match-every SlidingAgg), no in-budget sample may be counted late,
// and no edge may be stamped before the edge it follows.
func TestTrailingWindowKindsReachTheEventTimeLevel(t *testing.T) {
	series := []string{"a", "b", "c", "d"}
	const trials = 600
	for ci, c := range windowOracleCases() {
		rng := rand.New(rand.NewSource(int64(4200 + ci)))
		accepted, raisedSeen, lowSeen, undecided := 0, 0, 0, 0
		wrongLevel, backStamped, undecidedDiffer := 0, 0, 0
		var firstWrong, firstBack string
		for draw := 0; draw < trials*6 && accepted < trials; draw++ {
			evs, order, ok := genWindowTrial(rng, c, series)
			if !ok {
				continue
			}
			accepted++
			want := map[string]bool{}
			decide := map[string]bool{}
			for _, s := range series {
				want[s], decide[s] = windowOracleLevel(c.rule, evs, s)
				switch {
				case !decide[s]:
					undecided++
				case want[s]:
					raisedSeen++
				default:
					lowSeen++
				}
			}
			for _, k := range []int{1, 3} {
				var det detector
				if k == 1 {
					det = NewEngine([]Rule{c.rule}, c.lateness)
				} else {
					det = NewSharded(k, []Rule{c.rule}, c.lateness)
				}
				out := feedWindowTrial(det, evs, order)
				if late := det.DrainLateSamples(); late != 0 {
					t.Fatalf("%s K=%d: %d in-budget samples were counted late", c.name, k, late)
				}
				got := perSeriesDetections(out)
				for _, s := range series {
					ds := got[s]
					for i := 1; i < len(ds); i++ {
						if ds[i].At.Before(ds[i-1].At) {
							backStamped++
							if firstBack == "" {
								firstBack = fmt.Sprintf("K=%d series %s\n arrival: %s\n got: %s", k, s, arrivalOf(evs, order, s), fmtDets(ds))
							}
							break
						}
					}
					lvl := len(ds) > 0 && ds[len(ds)-1].Edge == EdgeRaised
					if !decide[s] {
						if lvl != want[s] {
							undecidedDiffer++
						}
						continue
					}
					if lvl != want[s] {
						wrongLevel++
						if firstWrong == "" {
							firstWrong = fmt.Sprintf("K=%d series %s: final raised=%v, event-time walk says raised=%v\n arrival: %s\n got: %s",
								k, s, lvl, want[s], arrivalOf(evs, order, s), fmtDets(ds))
						}
					}
				}
			}
		}
		if wrongLevel > 0 {
			t.Errorf("%s: %d series (of %d, both K) ended at a level the event-time walk does not reach; first:\n%s", c.name, wrongLevel, 2*accepted*len(series), firstWrong)
		}
		if backStamped > 0 {
			t.Errorf("%s: %d series (both K) emitted an edge stamped before the edge it follows; first:\n%s", c.name, backStamped, firstBack)
		}
		t.Logf("%s: %d trials; decidable finals %d raised / %d low; undecidable %d (of which %d differ from the walk)",
			c.name, accepted, raisedSeen, lowSeen, undecided, undecidedDiffer/2)
		if accepted < trials/2 || raisedSeen < 100 || lowSeen < 100 {
			t.Fatalf("%s: generator too weak: %d trials, %d raised and %d low decidable finals", c.name, accepted, raisedSeen, lowSeen)
		}
		if c.matchPct == 100 && undecided != 0 {
			t.Fatalf("%s: a match-every rule must always be decidable, %d were not", c.name, undecided)
		}
	}
}

func perSeriesDetections(ds []Detection) map[string][]Detection {
	m := map[string][]Detection{}
	for _, d := range ds {
		m[d.Series] = append(m[d.Series], d)
	}
	return m
}

func fmtDets(ds []Detection) string {
	s := ""
	for _, d := range ds {
		s += fmtDet(d) + " "
	}
	return s
}

func arrivalOf(evs []Event, order []int, s string) string {
	out := ""
	for _, idx := range order {
		e := evs[idx]
		if e.Key.Series != s {
			continue
		}
		out += fmt.Sprintf("(%ds v=%v m=%v%s) ", int(e.Time.Sub(base).Seconds()), e.Value, e.Match, map[bool]string{true: " " + e.Member, false: ""}[e.Member != ""])
	}
	return out
}

func feedOne(e *Engine, seq uint64, ev Event) []Detection {
	ev.Seq = seq
	e.ProcessResolved(seq, ev.Time, []Event{ev})
	return e.Drain()
}

// TestLateSampleThatLowersAnAverageResolvesAtTheNewest is the stuck-raised case: the raise comes
// from the newer sample, the older one arrives after it and pulls the average back under the line.
// Its own time is before the raise, so resolving AT its time would be refused as a stale falling
// edge and the alarm would stay raised over a window that does not breach. It resolves at the
// series' newest instant instead.
func TestLateSampleThatLowersAnAverageResolvesAtTheNewest(t *testing.T) {
	r := Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50}
	e := NewEngine([]Rule{r}, 3*time.Second)
	k := SeriesKey{Rule: "r", Series: "d"}
	if d := feedOne(e, 1, Event{Key: k, Time: at(4), Value: 70, HasValue: true, Match: true}); len(d) != 1 || d[0].Edge != EdgeRaised || !d[0].At.Equal(at(4)) {
		t.Fatalf("want Raised@4 on 70, got %+v", d)
	}
	d := feedOne(e, 2, Event{Key: k, Time: at(3), Value: 20, HasValue: true, Match: true})
	if len(d) != 1 || d[0].Edge != EdgeResolved || !d[0].At.Equal(at(4)) {
		t.Fatalf("the late 20@3 makes the window at 4 average 45: want Resolved@4, got %s", fmtDets(d))
	}
	if late := e.DrainLateSamples(); late != 0 {
		t.Fatalf("an in-budget sample is not late; counted %d", late)
	}
}

// TestLateSampleCannotJoinSamplesMoreThanAWindowApart is the window-shorter-than-lateness false
// alarm: 35 arrives after 39, inside the lateness budget, and a count taken over everything newer
// than ITS OWN window start pairs it with a sample four seconds away under a three-second window.
// The pair was never inside one window at any instant, so nothing may raise.
func TestLateSampleCannotJoinSamplesMoreThanAWindowApart(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		evs  []Event
	}{
		{"Repeating", Rule{ID: "r", Kind: Repeating, Count: 2, Window: 3 * time.Second}, []Event{
			{Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(39), Match: true},
			{Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(35), Match: true},
		}},
		{"Correlation", Rule{ID: "r", Kind: Correlation, Count: 2, Window: 3 * time.Second}, []Event{
			{Key: SeriesKey{Rule: "r", Series: "area"}, Member: "a", Time: at(39), Match: true},
			{Key: SeriesKey{Rule: "r", Series: "area"}, Member: "b", Time: at(35), Match: true},
		}},
	}
	for _, c := range cases {
		e := NewEngine([]Rule{c.rule}, 5*time.Second)
		var out []Detection
		for i, ev := range c.evs {
			out = append(out, feedOne(e, uint64(i+1), ev)...)
		}
		if len(out) != 0 {
			t.Fatalf("%s: 35 and 39 are 4 s apart under a 3 s window; want no edge, got %s", c.name, fmtDets(out))
		}
		if late := e.DrainLateSamples(); late != 0 {
			t.Fatalf("%s: 35 is inside the 5 s budget, so it is not late; counted %d", c.name, late)
		}
	}
}

// TestANewerNonMatchBoundsTheLateSamplesWindow: a non-matching reading folds nothing, so it opens
// no window — but it is still the series' newest instant, and later-arriving older samples are
// evaluated at it. Here the matches at 4 and 6 arrive after the non-match at 8: at 8 the 3 s
// window holds only 6, so the pair (which did coexist at 6, and was resolved by the reading at 8)
// must not leave the series raised.
func TestANewerNonMatchBoundsTheLateSamplesWindow(t *testing.T) {
	r := Rule{ID: "r", Kind: Repeating, Count: 2, Window: 3 * time.Second}
	e := NewEngine([]Rule{r}, 5*time.Second)
	k := SeriesKey{Rule: "r", Series: "d"}
	var out []Detection
	for i, ev := range []Event{{Key: k, Time: at(8)}, {Key: k, Time: at(4), Match: true}, {Key: k, Time: at(6), Match: true}} {
		out = append(out, feedOne(e, uint64(i+1), ev)...)
	}
	if len(out) != 0 {
		t.Fatalf("at the series' newest instant (8) the window holds one match; want no edge, got %s", fmtDets(out))
	}
}

// TestTheSeriesNewestSurvivesASnapshot: the newest instant is engine state, and an entry-less
// series (only a non-match so far) has nowhere else to keep it. A restore must bring it back, or
// the same arrival splits differently across a checkpoint — a replay-determinism break.
func TestTheSeriesNewestSurvivesASnapshot(t *testing.T) {
	r := Rule{ID: "r", Kind: Repeating, Count: 2, Window: 3 * time.Second}
	k := SeriesKey{Rule: "r", Series: "d"}
	e := NewEngine([]Rule{r}, 5*time.Second)
	feedOne(e, 1, Event{Key: k, Time: at(8)})
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Restore([]Rule{r}, 5*time.Second, b)
	if err != nil {
		t.Fatal(err)
	}
	var out []Detection
	out = append(out, feedOne(e2, 2, Event{Key: k, Time: at(4), Match: true})...)
	out = append(out, feedOne(e2, 3, Event{Key: k, Time: at(6), Match: true})...)
	if len(out) != 0 {
		t.Fatalf("restored: want no edge (the restored newest is 8), got %s", fmtDets(out))
	}
	s2 := NewSharded(3, []Rule{r}, 5*time.Second)
	s2.ProcessResolved(1, at(8), []Event{{Seq: 1, Key: k, Time: at(8)}})
	sb, err := s2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(sb) != string(b) {
		t.Fatalf("a sharded snapshot must carry the newest instant exactly as a single engine's does\n plain:   %s\n sharded: %s", b, sb)
	}
	rs, err := RestoreSharded(3, []Rule{r}, 5*time.Second, sb)
	if err != nil {
		t.Fatal(err)
	}
	rs.ProcessResolved(2, at(4), []Event{{Seq: 2, Key: k, Time: at(4), Match: true}})
	rs.ProcessResolved(3, at(6), []Event{{Seq: 3, Key: k, Time: at(6), Match: true}})
	if d := rs.Drain(); len(d) != 0 {
		t.Fatalf("restored sharded: want no edge, got %s", fmtDets(d))
	}
}

// TestASnapshotWithoutNewestRestoresSanely: a checkpoint written before the newest instant existed
// carries none. The restore recovers a lower bound from what the snapshot does hold — the newest
// retained sample (each kind keeps its own) and the rising edge — so a late sample right after an
// upgrade is still evaluated at the series' newest instant rather than back at its own time, where
// a raise would be stamped in the past and a resolve refused as stale, leaving the alarm stuck.
// Each case is built so that exactly one of those sources holds the series' newest instant.
func TestASnapshotWithoutNewestRestoresSanely(t *testing.T) {
	d := SeriesKey{Rule: "r", Series: "d"}
	area := SeriesKey{Rule: "r", Series: "area"}
	cases := []struct {
		name  string
		rule  Rule
		setup []Event
		late  Event
		want  string
	}{
		{"sliding window holds the newest", Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50},
			[]Event{{Key: d, Time: at(4), Value: 70, HasValue: true, Match: true}, {Key: d, Time: at(6), Value: 60, HasValue: true, Match: true}},
			Event{Key: d, Time: at(5), Value: 0, HasValue: true, Match: true}, "Resolved@6 "},
		{"repeating buffer holds the newest", Rule{ID: "r", Kind: Repeating, Count: 3, Window: 10 * time.Second},
			[]Event{{Key: d, Time: at(100), Match: true}, {Key: d, Time: at(109), Match: true}},
			Event{Key: d, Time: at(101), Match: true}, "Raised@109=0 "},
		{"cohort holds the newest", Rule{ID: "r", Kind: Correlation, Count: 3, Window: 10 * time.Second},
			[]Event{{Key: area, Member: "a", Time: at(100), Match: true}, {Key: area, Member: "b", Time: at(109), Match: true}},
			Event{Key: area, Member: "c", Time: at(101), Match: true}, "Raised@109=0 "},
		// The raise was made at a newer NON-match (8) by a late match (6): only the rising edge
		// remembers 8.
		{"the rising edge holds the newest", Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50},
			[]Event{{Key: d, Time: at(8)}, {Key: d, Time: at(6), Value: 70, HasValue: true, Match: true}},
			Event{Key: d, Time: at(5), Value: 20, HasValue: true, Match: true}, "Resolved@8 "},
	}
	for _, c := range cases {
		e := NewEngine([]Rule{c.rule}, 5*time.Second)
		for i, ev := range c.setup {
			feedOne(e, uint64(i+1), ev)
		}
		b, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["newest"]; !ok {
			t.Fatalf("%s: the snapshot has no newest section to strip; keys: %v", c.name, keysOf(m))
		}
		delete(m, "newest")
		old, _ := json.Marshal(m)
		e2, err := Restore([]Rule{c.rule}, 5*time.Second, old)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmtDets(feedOne(e2, uint64(len(c.setup)+1), c.late)); got != c.want {
			t.Fatalf("%s: restored from a pre-newest snapshot: want %q, got %q", c.name, c.want, got)
		}
	}
}

// TestALateRaiseLatchesTheInstantItIsStampedAt: the latch remembers the rising edge's instant, and
// it is what a later clamped resolve (a descope) and a restore's seed read. A Repeating run completed
// by a late sample is stamped at the newest instant, so that is the instant it must latch.
func TestALateRaiseLatchesTheInstantItIsStampedAt(t *testing.T) {
	r := Rule{ID: "r", Kind: Repeating, Count: 2, Window: 10 * time.Second}
	e := NewEngine([]Rule{r}, 5*time.Second)
	k := SeriesKey{Rule: "r", Series: "d"}
	feedOne(e, 1, Event{Key: k, Time: at(105), Match: true})
	if d := feedOne(e, 2, Event{Key: k, Time: at(101), Match: true}); fmtDets(d) != "Raised@105=0 " {
		t.Fatalf("want Raised@105, got %s", fmtDets(d))
	}
	if got := e.raised[k]; !got.Equal(at(105)) {
		t.Fatalf("the latch must hold the instant the raise was stamped at (105), got %v", got)
	}
	e.Descope("r", "d", at(102))
	if d := e.Drain(); fmtDets(d) != "Resolved@105 " {
		t.Fatalf("a descope behind the raise clamps to it: want Resolved@105, got %s", fmtDets(d))
	}
}

// TestADescopedSeriesForgetsItsNewestInstant: a descope tears down every piece of the series' state,
// the newest instant included. A series that re-enters scope starts fresh, so its first samples are
// read at their own time, not at an instant from before it left.
func TestADescopedSeriesForgetsItsNewestInstant(t *testing.T) {
	r := Rule{ID: "r", Kind: Repeating, Count: 2, Window: 10 * time.Second}
	e := NewEngine([]Rule{r}, 10*time.Second)
	k := SeriesKey{Rule: "r", Series: "d"}
	feedOne(e, 1, Event{Key: k, Time: at(108)})
	e.Descope("r", "d", at(108))
	e.Drain()
	feedOne(e, 2, Event{Key: k, Time: at(100), Match: true})
	if d := feedOne(e, 3, Event{Key: k, Time: at(101), Match: true}); fmtDets(d) != "Raised@101=0 " {
		t.Fatalf("after a descope the series starts fresh: want Raised@101, got %s", fmtDets(d))
	}
}

// TestALateSampleOutsideTheNewestWindowCannotRaise: a matching sample inside the lateness budget but
// older than the window open at the series' newest instant folds nothing, so it raises nothing —
// even over a window that satisfies. The window here satisfies only because a non-match aged the
// old high reading out, which by the kind's rule does not raise; the late match must not either.
func TestALateSampleOutsideTheNewestWindowCannotRaise(t *testing.T) {
	r := Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMax, Op: LT, Thresh: 60}
	e := NewEngine([]Rule{r}, 6*time.Second)
	k := SeriesKey{Rule: "r", Series: "d"}
	var out []Detection
	for i, ev := range []Event{
		{Key: k, Time: at(1), Value: 80, HasValue: true, Match: true},
		{Key: k, Time: at(4), Value: 30, HasValue: true, Match: true},
		{Key: k, Time: at(7)},
		{Key: k, Time: at(2), Value: 10, HasValue: true, Match: true},
	} {
		out = append(out, feedOne(e, uint64(i+1), ev)...)
	}
	if len(out) != 0 {
		t.Fatalf("want no edge (the event-time walk never raises), got %s", fmtDets(out))
	}
	if late := e.DrainLateSamples(); late != 0 {
		t.Fatalf("2 is inside the budget, so it is not late; counted %d", late)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
