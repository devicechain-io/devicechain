// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Two readings that share a timestamp, the first clearing the rule and the second breaching
// it, must not emit Resolved@T then Raised@T: the alarm fold lets a resolve win an
// equal-time raise, which would leave the alarm CLEARED while the latch stays raised and
// suppresses every later breach. The fresh raise is stamped one microsecond after the fall.
func TestThresholdSameInstantFallThenRise(t *testing.T) {
	for _, first := range []int{1, 2} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("first=%d/restart=%v", first, restart), func(t *testing.T) {
				e := NewEngine([]Rule{{ID: "r", Kind: Threshold}}, time.Minute)
				initial := feedEvent(e, 1, "r", "d", first, 90, true)
				fall := feedEvent(e, 2, "r", "d", 2, 70, false)
				if len(fall) != 1 || fall[0].Edge != EdgeResolved || !fall[0].At.Equal(at(2)) {
					t.Fatalf("want resolve@T: %+v", fall)
				}
				if restart {
					data, err := e.Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					e, err = Restore([]Rule{{ID: "r", Kind: Threshold}}, time.Minute, data)
					if err != nil {
						t.Fatal(err)
					}
				}
				rise := feedEvent(e, 3, "r", "d", 2, 95, true)
				want := at(2).Add(time.Microsecond)
				if len(rise) != 1 || rise[0].Edge != EdgeRaised || !rise[0].At.Equal(want) {
					t.Fatalf("first=%d restart=%v: want raise@T+1us, got %+v", first, restart, rise)
				}
				if initial[0].At.Equal(rise[0].At) {
					t.Fatal("separate episodes must have distinct dedup times")
				}
				key := SeriesKey{Rule: "r", Series: "d"}
				if !e.raised[key].Equal(want) || !e.raiseInput(key).Equal(at(2)) {
					t.Fatalf("latch must hold the bumped stamp and the input instant: %v / %v", e.raised[key], e.raiseInput(key))
				}
				// The input instant still decides staleness: a healthy fall at T resolves.
				again := feedEvent(e, 4, "r", "d", 2, 60, false)
				if len(again) != 1 || again[0].Edge != EdgeResolved || !again[0].At.Equal(want) {
					t.Fatalf("a fall at the input instant must resolve, clamped to the bumped raise: %+v", again)
				}
			})
		}
	}
}

// A second same-instant cycle: the clamped resolve's emitted time is T+1us but its decision
// time is T, so the next raise at T must be stamped past the emitted fall, not left at T.
func TestDeltaRateRepeatedSameInstantCycles(t *testing.T) {
	r := Rule{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}
	e := NewEngine([]Rule{r}, 0)
	key := SeriesKey{Rule: "r", Series: "d"}
	var all []Detection
	for i, s := range []struct {
		sec   int
		value float64
	}{{1, 0}, {2, 100}, {3, 0}, {3, 100}, {3, 101}, {3, 201}} {
		ev := Event{Key: key, Time: at(s.sec), Value: s.value, HasValue: true, Match: true}
		e.ProcessResolved(uint64(i+1), ev.Time, []Event{ev})
		all = append(all, e.Drain()...)
	}
	e.Advance(at(100))
	all = append(all, e.Drain()...)
	if got := fmtDets(all); got != "Raised@2=100 Resolved@3 Raised@3.000001=100 Resolved@3.000001 Raised@3.000002=100 " {
		t.Fatalf("repeated same-instant cycles: %s", got)
	}
	assertStrictEpisodeEdges(t, all)
}

func TestFallingMemoRetentionAndOrdinaryRaise(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		for _, age := range []time.Duration{thresholdCursorHorizon, thresholdCursorHorizon + time.Nanosecond} {
			r := Rule{ID: "r", Kind: Threshold}
			e := NewEngine([]Rule{r}, 0)
			key := SeriesKey{Rule: "r", Series: "d"}
			e.emit(r, key, at(1))
			if d := e.Drain(); len(d) != 1 || !d[0].At.Equal(at(1)) {
				t.Fatalf("ordinary raise changed: %+v", d)
			}
			e.Advance(at(2))
			e.resolve(r, key, at(2))
			e.Drain()
			e.Advance(at(2).Add(age))
			if sweep {
				data, err := e.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				var memo struct {
					Fell []json.RawMessage `json:"fell"`
				}
				if err := json.Unmarshal(data, &memo); err != nil {
					t.Fatal(err)
				}
				if kept := len(memo.Fell) != 0; kept != (age == thresholdCursorHorizon) {
					t.Fatalf("snapshot retention at age %v: kept=%v", age, kept)
				}
			}
			e.emit(r, key, at(2))
			want := at(2)
			if age == thresholdCursorHorizon {
				want = want.Add(time.Microsecond)
			}
			if d := e.Drain(); len(d) != 1 || !d[0].At.Equal(want) {
				t.Fatalf("sweep=%v age=%v: want %v got %+v", sweep, age, want, d)
			}
		}
	}
}

// A raise strictly after the fall keeps its own time and records no separate input.
func TestRaiseAfterFallKeepsOwnTime(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold}}, time.Minute)
	feedEvent(e, 1, "r", "d", 1, 90, true)
	feedEvent(e, 2, "r", "d", 2, 70, false)
	got := feedEvent(e, 3, "r", "d", 3, 95, true)
	if len(got) != 1 || got[0].Edge != EdgeRaised || !got[0].At.Equal(at(3)) {
		t.Fatalf("a later raise must keep its own time: %+v", got)
	}
	if _, ok := e.raiseInputs[SeriesKey{Rule: "r", Series: "d"}]; ok {
		t.Fatal("an unbumped raise must record no separate input")
	}
}

func TestThresholdStrictlyStaleRaiseKeepsOwnTime(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Threshold}}, time.Minute)
	feedEvent(e, 1, "r", "d", 1, 90, true)
	feedEvent(e, 2, "r", "d", 3, 70, false)
	got := feedEvent(e, 3, "r", "d", 2, 95, true)
	if len(got) != 1 || got[0].Edge != EdgeRaised || !got[0].At.Equal(at(2)) {
		t.Fatalf("stale raise must remain behind the resolve@3: %+v", got)
	}
}

// A clamped fall (decided at T, emitted at a later raise) bumps a raise anywhere in
// [decision, emitted], and not one strictly before the decision.
func TestClampedFallBumpBounds(t *testing.T) {
	r := Rule{ID: "r", Kind: Threshold}
	key := SeriesKey{Rule: "r", Series: "d"}
	for _, c := range []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"before-decision", at(2).Add(-time.Nanosecond), at(2).Add(-time.Nanosecond)},
		{"at-decision", at(2), at(3).Add(time.Microsecond)},
		{"between", at(2).Add(500 * time.Millisecond), at(3).Add(time.Microsecond)},
		{"at-emitted", at(3), at(3).Add(time.Microsecond)},
		{"after-emitted", at(3).Add(time.Nanosecond), at(3).Add(time.Nanosecond)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := NewEngine([]Rule{r}, 0)
			e.emit(r, key, at(3))
			e.resolveLatched(r, key, at(2)) // a session-dominant resolve stamped before the raise
			if d := e.Drain(); len(d) != 2 || !d[1].At.Equal(at(3)) {
				t.Fatalf("setup: want a resolve clamped to the raise: %+v", d)
			}
			e.emit(r, key, c.at)
			if d := e.Drain(); len(d) != 1 || !d[0].At.Equal(c.want) {
				t.Fatalf("raise at %v: want %v, got %+v", c.at, c.want, d)
			}
		})
	}
}

func TestFallingMemoSnapshotAcrossShardCounts(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	for _, shards := range []int{1, 4} {
		e := NewSharded(shards, rules, time.Minute)
		for i, match := range []bool{true, false} {
			ev := Event{Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(i + 1), Match: match}
			e.ProcessResolved(uint64(i+1), ev.Time, []Event{ev})
			e.Drain()
		}
		data, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, restoredShards := range []int{1, 4} {
			r, err := RestoreSharded(restoredShards, rules, time.Minute, data)
			if err != nil {
				t.Fatal(err)
			}
			r.ProcessResolved(3, at(2), []Event{{Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(2), Match: true}})
			if d := r.Drain(); len(d) != 1 || !d[0].At.Equal(at(2).Add(time.Microsecond)) {
				t.Fatalf("K=%d -> %d lost memo: %+v", shards, restoredShards, d)
			}
		}
	}
}

// A single frontier jump may finalize multiple episodes in one held event-time run.
func TestDeltaRateFallAndRiseSurviveFarFutureFlush(t *testing.T) {
	r := Rule{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}
	e := NewEngine([]Rule{r}, time.Minute)
	key := SeriesKey{Rule: "r", Series: "d"}
	for i, s := range []struct {
		sec   int
		value float64
	}{{1, 0}, {2, 100}, {3, 0}, {3, 100}} {
		ev := Event{Key: key, Time: at(s.sec), Value: s.value, HasValue: true, Match: true}
		e.ProcessResolved(uint64(i+1), ev.Time, []Event{ev})
	}
	e.Advance(at(10000))
	if got := fmtDets(e.Drain()); got != "Raised@2=100 Resolved@3 Raised@3.000001=100 " {
		t.Fatalf("held run lost its falling-edge memo: %s", got)
	}
}

// The timestamp used for dedup is not a newer input sample for window evaluation.
func TestBumpedSlidingRaisePreservesEvaluationInstant(t *testing.T) {
	r := Rule{ID: "r", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggAvg, Op: LT, Thresh: 40}
	e := NewEngine([]Rule{r}, 3*time.Second)
	key := SeriesKey{Rule: "r", Series: "d"}
	for i, s := range []struct {
		sec   int
		value float64
	}{{10, 10}, {12, 100}, {12, 0}} {
		ev := Event{Key: key, Time: at(s.sec), Value: s.value, HasValue: true, Match: true}
		e.ProcessResolved(uint64(i+1), ev.Time, []Event{ev})
		e.Drain()
	}
	if !e.raised[key].Equal(at(12).Add(time.Microsecond)) {
		t.Fatal("setup must bump the second raise")
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore([]Rule{r}, 3*time.Second, data)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.newest[key].Equal(at(12)) {
		t.Fatalf("restore moved the input instant: %s", restored.newest[key])
	}
	for _, d := range []*Engine{e, restored} {
		ev := Event{Key: key, Time: at(11), Value: 100, HasValue: true, Match: true}
		d.ProcessResolved(4, ev.Time, []Event{ev})
		if got := d.Drain(); len(got) != 0 {
			t.Fatalf("late resolve must be held: %+v", got)
		}
		d.Advance(at(18))
		ds := d.Drain()
		if len(ds) != 1 || ds[0].Edge != EdgeResolved || !ds[0].At.Equal(at(12).Add(time.Microsecond)) {
			t.Fatalf("held resolve must fire at its original deadline and clamp to the bumped raise: %+v", ds)
		}
	}
}

// A dedup bump must not turn an equal-time input decision into a stale fall.
func TestBumpedDeltaRaiseResolvesAtInputTimeAfterRestore(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}}
	key := SeriesKey{Rule: "r", Series: "d"}
	for _, k := range []int{1, 4} {
		e := NewSharded(k, rules, 0)
		for i, s := range []struct {
			sec   int
			value float64
		}{{1, 0}, {2, 100}, {3, 0}, {3, 100}} {
			ev := Event{Key: key, Time: at(s.sec), Value: s.value, HasValue: true, Match: true}
			e.ProcessResolved(uint64(i+1), ev.Time, []Event{ev})
			e.Drain()
		}
		e.Advance(at(4))
		e.Drain()
		data, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, restoredK := range []int{1, 4} {
			restored, err := RestoreSharded(restoredK, rules, 0, data)
			if err != nil {
				t.Fatal(err)
			}
			ev := Event{Key: key, Time: at(3), Value: 101, HasValue: true, Match: true}
			restored.ProcessResolved(5, at(4), []Event{ev})
			restored.Advance(at(5))
			got := restored.Drain()
			if len(got) != 1 || got[0].Edge != EdgeResolved || !got[0].At.Equal(at(3).Add(time.Microsecond)) {
				t.Fatalf("K=%d -> %d: valid input-time fall must clamp to bumped raise: %+v", k, restoredK, got)
			}
		}
	}
}

func TestDescopeReentryRetainsFallingMemo(t *testing.T) {
	for _, restart := range []bool{false, true} {
		rules := []Rule{{ID: "r", Kind: Threshold}}
		e := NewEngine(rules, 0)
		feedEvent(e, 1, "r", "d", 1, 90, true)
		e.Descope("r", "d", at(2))
		if got := e.Drain(); len(got) != 1 || got[0].Edge != EdgeResolved || !got[0].At.Equal(at(2)) {
			t.Fatalf("leave scope must resolve@2: %+v", got)
		}
		if restart {
			data, err := e.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			e, err = Restore(rules, 0, data)
			if err != nil {
				t.Fatal(err)
			}
		}
		got := feedEvent(e, 2, "r", "d", 2, 95, true)
		if len(got) != 1 || got[0].Edge != EdgeRaised || !got[0].At.Equal(at(2).Add(time.Microsecond)) {
			t.Fatalf("restart=%v: reentry must raise after scope resolve: %+v", restart, got)
		}
	}
}

func TestLaggingFallingMemoRecordingFrontierSurvivesRestore(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	key := SeriesKey{Rule: "r", Series: "d"}
	e := NewEngine(rules, 0)
	e.Advance(at(10000))
	e.emit(rules[0], key, at(1))
	e.resolve(rules[0], key, at(2))
	e.Drain()
	data, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{thresholdCursorHorizon, thresholdCursorHorizon + time.Nanosecond} {
		restored, err := Restore(rules, 0, data)
		if err != nil {
			t.Fatal(err)
		}
		restored.Advance(at(10000).Add(age))
		restored.emit(rules[0], key, at(2))
		want := at(2)
		if age == thresholdCursorHorizon {
			want = want.Add(time.Microsecond)
		}
		if got := restored.Drain(); len(got) != 1 || !got[0].At.Equal(want) {
			t.Fatalf("age=%v: want %v, got %+v", age, want, got)
		}
	}
	// A memo without its recording frontier ages from its edge time.
	var old snapshot
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	old.Fell[0].RecordedFrontier = nil
	legacy := restoreState(rules, 0, old)
	legacy.emit(rules[0], key, at(2))
	if got := legacy.Drain(); len(got) != 1 || !got[0].At.Equal(at(2)) {
		t.Fatalf("old memo must expire from its edge time: %+v", got)
	}
}

// assertStrictEpisodeEdges checks the class-wide edge contract: per key, edges alternate,
// start with a raise, and a fresh episode strictly follows the preceding resolve.
func assertStrictEpisodeEdges(t *testing.T, ds []Detection) {
	t.Helper()
	last := map[SeriesKey]Detection{}
	for _, d := range ds {
		key := SeriesKey{Rule: d.RuleID, Series: d.Series}
		if prev, ok := last[key]; ok {
			if d.Edge == prev.Edge {
				t.Fatalf("key %+v: edges must alternate: %+v then %+v", key, prev, d)
			}
			if d.Edge == EdgeRaised && !d.At.After(prev.At) {
				t.Fatalf("key %+v: a raise must strictly follow its preceding resolve: %+v then %+v", key, prev, d)
			}
		} else if d.Edge != EdgeRaised {
			t.Fatalf("key %+v starts with resolve", key)
		}
		last[key] = d
	}
}

// Every kind shares the same three raise primitives. Exercise all of them with equal-time
// episode boundaries, independent of each kind's window arithmetic.
func TestEveryKindHasStrictEpisodeEdges(t *testing.T) {
	for kind := Threshold; kind <= Connectivity; kind++ {
		r := Rule{ID: "r", Kind: kind}
		e := NewEngine([]Rule{r}, 0)
		key := SeriesKey{Rule: "r", Series: "d"}
		e.emit(r, key, at(1))
		e.resolve(r, key, at(2))
		e.emitValue(r, key, at(2), 42)
		e.resolve(r, key, at(3))
		e.emitSampleAt(r, Event{Key: key, Value: 43, HasValue: true}, at(3))
		e.resolve(r, key, at(4))
		ds := e.Drain()
		if len(ds) != 6 {
			t.Fatalf("kind %v: want six edges, got %+v", kind, ds)
		}
		assertStrictEpisodeEdges(t, ds)
	}
}
