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

// What the one-timer-per-key wheel and the lean sliding window promise about MEMORY. The
// differential test (timers_windows_differential_test.go) pins that nothing observable moved;
// these pin that the memory did, and that the structures hold under the operations a wheel
// serves: reschedule, cancel, fire, fire-then-rearm, purge.

// TestAbsenceHoldsOneTimerPerKeyNotPerEvent: the heap is O(keys), however many events arrive.
// At every checkpoint of a long run the count equals the number of armed keys; the old wheel
// would be at the event count here.
func TestAbsenceHoldsOneTimerPerKeyNotPerEvent(t *testing.T) {
	const id = "acme/p@1/abs"
	const keys = 40
	const events = 200_000
	e := NewEngine([]Rule{{ID: id, Kind: Absence, Timeout: time.Hour}}, 0)
	for i := 1; i <= events; i++ {
		e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: fmt.Sprintf("d%d", i%keys)}, Time: at(i), Match: true})
		if i%5000 == 0 {
			e.Drain()
			if got, want := e.PendingTimerCount(), min(i, keys); got != want {
				t.Fatalf("after %d events: %d timer entries, want %d (one per armed key)", i, got, want)
			}
		}
	}
}

// TestSessionHoldsOneTimerPerKeyNotPerEvent is the same pin for the other scheduleForward caller.
func TestSessionHoldsOneTimerPerKeyNotPerEvent(t *testing.T) {
	const id = "acme/p@1/sess"
	const keys = 25
	e := NewEngine([]Rule{{ID: id, Kind: Session, Gap: time.Hour, Agg: AggCount, Op: GE, Thresh: 1}}, 0)
	for i := 1; i <= 100_000; i++ {
		e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: fmt.Sprintf("d%d", i%keys)}, Time: at(i), Match: true, Value: 1, HasValue: true})
	}
	e.Drain()
	if got := e.PendingTimerCount(); got != keys {
		t.Fatalf("%d timer entries after 100000 events over %d sessions, want %d", got, keys, keys)
	}
}

// TestFiredAbsenceKeyRearms: a key whose timer fired is no longer in the heap, and a later event
// arms it afresh and it fires again. A wheel that kept the fired entry in its index would try to
// move an entry that is no longer in the heap.
func TestFiredAbsenceKeyRearms(t *testing.T) {
	const id = "acme/p@1/abs"
	k := SeriesKey{Rule: id, Series: "d1"}
	e := NewEngine([]Rule{{ID: id, Kind: Absence, Timeout: 10 * time.Second}}, 0)
	for round := 0; round < 4; round++ {
		off := round * 100
		e.ProcessEvent(Event{Seq: uint64(2*round + 1), Key: k, Time: at(off + 1), Match: true})
		if got := e.PendingTimerCount(); got != 1 {
			t.Fatalf("round %d: %d timers after a heartbeat, want 1", round, got)
		}
		e.Advance(at(off + 50))
		var fired []Detection
		for _, d := range e.Drain() {
			if d.Edge == EdgeRaised { // the next round's heartbeat first resolves the previous alarm
				fired = append(fired, d)
			}
		}
		if len(fired) != 1 || fired[0].Kind != Absence || !fired[0].At.Equal(at(off+11)) {
			t.Fatalf("round %d: want one absence stamped at its deadline, got %+v", round, fired)
		}
		if got := e.PendingTimerCount(); got != 0 {
			t.Fatalf("round %d: %d timers left after the key fired, want 0", round, got)
		}
	}
}

// TestWheelMatchesAReferenceModel drives the wheel with a random mix of schedule, scheduleForward,
// cancel, popDue and purge, and checks it against a plain map of key -> deadline: the same fired
// keys in the same (deadline, rule, series) order — equal deadlines included — the same armed
// set, and a heap of exactly the armed keys.
func TestWheelMatchesAReferenceModel(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		w := newTimerWheel()
		model := map[SeriesKey]time.Time{}
		keyOf := func() SeriesKey {
			return SeriesKey{Rule: fmt.Sprintf("r%d", rng.Intn(3)), Series: fmt.Sprintf("s%d", rng.Intn(8))}
		}
		now := 0
		for step := 0; step < 3000; step++ {
			k := keyOf()
			dl := at(now + rng.Intn(12)) // coarse: many equal deadlines
			switch rng.Intn(10) {
			case 0, 1, 2:
				w.schedule(k, dl)
				model[k] = dl
			case 3, 4, 5:
				w.scheduleForward(k, dl)
				if cur, ok := model[k]; !ok || dl.After(cur) {
					model[k] = dl
				}
			case 6:
				w.cancel(k)
				delete(model, k)
			case 7:
				rule := fmt.Sprintf("r%d", rng.Intn(3))
				w.purgeMatching(func(id string) bool { return id == rule })
				for mk := range model {
					if mk.Rule == rule {
						delete(model, mk)
					}
				}
			default:
				now += rng.Intn(4)
				wm := at(now)
				got := w.popDue(wm)
				var want []firedTimer
				for mk, d := range model {
					if !d.After(wm) {
						want = append(want, firedTimer{key: mk, deadline: d})
						delete(model, mk)
					}
				}
				sort.Slice(want, func(i, j int) bool {
					a, b := want[i], want[j]
					if !a.deadline.Equal(b.deadline) {
						return a.deadline.Before(b.deadline)
					}
					if a.key.Rule != b.key.Rule {
						return a.key.Rule < b.key.Rule
					}
					return a.key.Series < b.key.Series
				})
				if len(got) != len(want) {
					t.Fatalf("seed %d step %d: fired %d, want %d", seed, step, len(got), len(want))
				}
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("seed %d step %d: fired[%d] = %+v, want %+v", seed, step, i, got[i], want[i])
					}
				}
			}
			if w.pq.Len() != len(model) || len(w.live) != len(model) {
				t.Fatalf("seed %d step %d: heap %d / live %d, want %d armed keys", seed, step, w.pq.Len(), len(w.live), len(model))
			}
			for i, tm := range w.pq {
				if tm.index != i {
					t.Fatalf("seed %d step %d: heap entry %d believes it is at %d", seed, step, i, tm.index)
				}
			}
		}
	}
}

// TestWheelCancelRemovesTheEntry: cancel takes the entry out of the heap rather than leaving it.
func TestWheelCancelRemovesTheEntry(t *testing.T) {
	w := newTimerWheel()
	k := SeriesKey{Rule: "r", Series: "s"}
	w.schedule(k, at(5))
	w.schedule(SeriesKey{Rule: "r", Series: "other"}, at(6))
	w.cancel(k)
	if w.pq.Len() != 1 {
		t.Fatalf("heap holds %d entries after cancelling one of two, want 1", w.pq.Len())
	}
	if got := w.popDue(at(100)); len(got) != 1 || got[0].key.Series != "other" {
		t.Fatalf("cancelled key fired or the other was lost: %+v", got)
	}
}

// TestWheelScheduleMovesEarlierAndLater: schedule (the Duration route) may move a deadline in
// either direction, and the key fires once, at the last deadline set.
func TestWheelScheduleMovesEarlierAndLater(t *testing.T) {
	w := newTimerWheel()
	k := SeriesKey{Rule: "r", Series: "s"}
	w.schedule(k, at(50))
	w.schedule(k, at(10)) // earlier
	if got := w.popDue(at(20)); len(got) != 1 || !got[0].deadline.Equal(at(10)) {
		t.Fatalf("want one firing at t=10, got %+v", got)
	}
	w.schedule(k, at(30))
	w.schedule(k, at(40)) // later
	if got := w.popDue(at(35)); len(got) != 0 {
		t.Fatalf("fired at the superseded deadline: %+v", got)
	}
	if got := w.popDue(at(45)); len(got) != 1 || !got[0].deadline.Equal(at(40)) {
		t.Fatalf("want one firing at t=40, got %+v", got)
	}
	if w.pq.Len() != 0 {
		t.Fatalf("heap not empty: %d", w.pq.Len())
	}
}

// retainedDeques reports how many deque entries the engine holds across all its windows.
func retainedDeques(e *Engine) (n int) {
	for _, st := range e.slides {
		n += cap(st.minDq) + cap(st.maxDq)
	}
	return n
}

// TestSumAvgCountWindowsBuildNoDeques: a window whose rule reads no min/max never allocates a
// deque, whether samples arrive in order, late, or after a restore; min and max windows do.
func TestSumAvgCountWindowsBuildNoDeques(t *testing.T) {
	feed := func(e *Engine, id string) {
		rng := rand.New(rand.NewSource(3))
		for i := 1; i <= 3000; i++ {
			ts := i
			if rng.Intn(4) == 0 {
				ts -= rng.Intn(4) // late: the splice path
			}
			e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: "d"}, Time: at(ts), Value: float64(rng.Intn(20)), HasValue: true, Match: true})
		}
		e.Drain()
	}
	for _, c := range []struct {
		agg  AggOp
		deqs bool
	}{{AggSum, false}, {AggAvg, false}, {AggCount, false}, {AggMin, true}, {AggMax, true}} {
		id := fmt.Sprintf("acme/p@1/sl%d", c.agg)
		e := NewEngine([]Rule{{ID: id, Kind: SlidingAgg, Window: 500 * time.Second, Agg: c.agg, Op: GT, Thresh: 1e9}}, 5*time.Second)
		feed(e, id)
		if got := retainedDeques(e) > 0; got != c.deqs {
			t.Fatalf("agg %d: deques built = %v, want %v", c.agg, got, c.deqs)
		}
		snap, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		re, err := Restore([]Rule{{ID: id, Kind: SlidingAgg, Window: 500 * time.Second, Agg: c.agg, Op: GT, Thresh: 1e9}}, 5*time.Second, snap)
		if err != nil {
			t.Fatal(err)
		}
		re.ProcessEvent(Event{Seq: 9999, Key: SeriesKey{Rule: id, Series: "d"}, Time: at(3001), Value: 1, HasValue: true, Match: true})
		if got := retainedDeques(re) > 0; got != c.deqs {
			t.Fatalf("agg %d after restore: deques built = %v, want %v", c.agg, got, c.deqs)
		}
	}
}

// TestLeanWindowAnswersMinMaxWhenAskedAndThenMaintainsThem: a window built lean (its rule read
// sum) that is later asked for min/max (the rule changed under the same key) gives the right
// answer, pays the rebuild once, and is maintained incrementally from then on.
func TestLeanWindowAnswersMinMaxWhenAskedAndThenMaintainsThem(t *testing.T) {
	s := &slidingState{lean: true}
	for i, v := range []float64{5, 2, 9, 7, 2, 11} {
		s.insert(sample{t: at(i + 1), v: v})
	}
	s.insert(sample{t: at(3).Add(time.Millisecond), v: -4}) // late
	if len(s.minDq) != 0 || len(s.maxDq) != 0 || !s.dirty {
		t.Fatal("a lean window must hold no deques and owe them")
	}
	if got := s.value(AggMin); got != -4 {
		t.Fatalf("min = %v, want -4", got)
	}
	if got := s.value(AggMax); got != 11 {
		t.Fatalf("max = %v, want 11", got)
	}
	if s.lean || s.dirty {
		t.Fatal("the first min/max read must pay the debt and end leanness")
	}
	s.insert(sample{t: at(10), v: -9})
	s.evict(at(2))
	if s.dirty {
		t.Fatal("after the debt is paid the deques are maintained, not re-owed")
	}
	if got := s.value(AggMin); got != -9 {
		t.Fatalf("min after more inserts = %v, want -9", got)
	}
}

// TestRestoreOfDuplicateTimersFiresTheEarlier: a snapshot listing two timers for one key (never
// written by snapshot(), but restore must stay total) fires the earlier deadline, as the
// push-per-event wheel did, and the key then holds no timer.
func TestRestoreOfDuplicateTimersFiresTheEarlier(t *testing.T) {
	k := SeriesKey{Rule: "a", Series: "d"}
	w := restoreTimerWheel(
		[]snapTimer{{Deadline: at(20), Rule: "a", Series: "d", Gen: 1}, {Deadline: at(10), Rule: "a", Series: "d", Gen: 1}},
		[]snapGen{{Rule: "a", Series: "d", Gen: 1}})
	if w.pq.Len() != 1 {
		t.Fatalf("heap holds %d entries for one key", w.pq.Len())
	}
	got := w.popDue(at(15))
	if len(got) != 1 || got[0].key != k || !got[0].deadline.Equal(at(10)) {
		t.Fatalf("want the t=10 deadline to fire, got %+v", got)
	}
	if w.pq.Len() != 0 || len(w.popDue(at(100))) != 0 {
		t.Fatal("the later duplicate must not fire after the earlier one did")
	}
}
