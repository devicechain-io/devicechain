// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// The three SLIDING kinds used to derive their eviction cutoff from the ARRIVING event's time
// alone. A store-and-forward device uploads samples stamped at the instants they were taken, so
// one of those rewinds the cutoff by however far behind the frontier it is — nothing ages out,
// the window keeps every live entry alongside the buffered one, and a "3 in 10 seconds" rule
// fires on evidence spanning an hour. windowCutoff clamps the cutoff to the frontier.
//
// Each case below drives the REAL path (ProcessResolved, which advances the watermark from the
// message time exactly as the live processor does) rather than calling the apply function, so it
// also pins that the frontier is where the apply functions can actually reach it.
func TestLateSampleDoesNotRewindTheSlidingCutoff(t *testing.T) {
	const lateness = 5 * time.Second

	t.Run("repeating", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, lateness)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
		e.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Time: at(105), Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("two events in a 10s window must not reach Count=3, got %+v", d)
		}
		// The buffered upload: one sample taken 65s ago. The frontier does not move (observe is
		// forward-only), so the window that is open is still the one around 100.
		e.ProcessResolved(3, at(40), []Event{{Seq: 3, Key: key, Time: at(40), Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Errorf("3-in-10s fired on events at 40, 100 and 105 — a 65s span: %+v", d)
		}
	})

	t.Run("slidingagg", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggMax, Op: GT, Thresh: 100}}, lateness)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Value: 50, HasValue: true, Match: true}})
		e.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Time: at(105), Value: 50, HasValue: true, Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("a trailing max of 50 must not breach a threshold of 100, got %+v", d)
		}
		e.ProcessResolved(3, at(40), []Event{{Seq: 3, Key: key, Time: at(40), Value: 500, HasValue: true, Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Errorf("trailing-10s max fired on a sample taken 65s before the frontier: %+v", d)
		}
	})

	t.Run("correlation", func(t *testing.T) {
		e := NewEngine([]Rule{{ID: "r", Kind: Correlation, Window: 10 * time.Second, Count: 3, MemberCap: 100}}, lateness)
		key := SeriesKey{Rule: "r", Series: "area"}
		e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Member: "devA", Time: at(100), Match: true}})
		e.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Member: "devB", Time: at(105), Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Fatalf("two distinct members must not reach Count=3, got %+v", d)
		}
		e.ProcessResolved(3, at(40), []Event{{Seq: 3, Key: key, Member: "devC", Time: at(40), Match: true}})
		if d := e.Drain(); len(d) != 0 {
			t.Errorf("3-distinct-in-10s fired on sightings spanning 65s: %+v", d)
		}
	})
}

// The admission boundary is the FRONTIER's cutoff (wm.now - Window), not the sample's own
// (ev.Time - Window). The distinction is the whole fix and it is invisible in the false-raise
// cases above, which any late-drop guard would also pass. A guard shaped like applySession's —
// refuse a sample whose own window has passed the frontier — admits everything after
// wm.now-Window*2 and so still lets a rewound cutoff stretch the window to twice its length.
//
// With wm.now=100 and Window=10s the boundary sits at exactly 90: a sample at 90 is refused
// (evict is at-or-before), 91 is admitted. A session-shaped guard would put it at 80.
func TestSlidingAdmissionBoundaryIsTheFrontierNotTheSample(t *testing.T) {
	for _, tc := range []struct {
		sec      int
		admitted bool
	}{
		{80, false}, // admitted by a session-shaped guard; must be refused here
		{85, false},
		{89, false},
		{90, false}, // exactly at the cutoff: evict drops at-or-before, so this cannot be folded in
		{91, true},  // first instant inside the frontier's window
		{99, true},
	} {
		e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, 5*time.Second)
		key := SeriesKey{Rule: "r", Series: "dev"}
		e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
		e.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Time: at(105), Match: true}})
		e.Drain()
		e.ProcessResolved(3, at(tc.sec), []Event{{Seq: 3, Key: key, Time: at(tc.sec), Match: true}})
		fired, late := len(e.Drain()) > 0, e.DrainLateSamples()
		if fired != tc.admitted {
			t.Errorf("sample@%d: folded in = %v, want %v (frontier 100, cutoff 90)", tc.sec, fired, tc.admitted)
		}
		if wantLate := uint64(0); tc.admitted && late != wantLate {
			t.Errorf("sample@%d: counted %d late, want %d", tc.sec, late, wantLate)
		}
		if wantLate := uint64(1); !tc.admitted && late != wantLate {
			t.Errorf("sample@%d: counted %d late, want %d", tc.sec, late, wantLate)
		}
	}
}

// The counterweight. Refusing late samples is only correct while BOUNDED out-of-orderness still
// works: the engine advertises a lateness budget and a sample inside it must fold in normally, or
// the fix has quietly become "drop everything that is not perfectly ordered" and every real device
// with jitter loses readings. Here the frontier lags 5s behind the newest sample, so a reading 4s
// out of order is well inside the budget and completes the count.
func TestSampleInsideTheLatenessBudgetStillFoldsIn(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, 5*time.Second)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
	e.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Time: at(105), Match: true}})
	e.Drain()
	// Out of order by 4s against the newest sample, and 1s ahead of the frontier.
	e.ProcessResolved(3, at(101), []Event{{Seq: 3, Key: key, Time: at(101), Match: true}})
	if d := e.Drain(); len(d) != 1 {
		t.Fatalf("a sample inside the lateness budget must complete the count, got %+v", d)
	}
	if n := e.DrainLateSamples(); n != 0 {
		t.Errorf("an in-budget sample must not be counted late, got %d", n)
	}
}

// The no-op control. An entirely in-order run must be unaffected by the clamp — for such an event
// ev.Time is at or ahead of the frontier, so max(ev.Time, wm.now) IS ev.Time and the cutoff is
// what it always was. If this ever diverges, the clamp has started changing the common path.
func TestInOrderTrafficIsUnaffectedByTheClamp(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, 5*time.Second)
	key := SeriesKey{Rule: "r", Series: "dev"}
	for i, sec := range []int{100, 101, 102, 103} {
		e.ProcessResolved(uint64(i+1), at(sec), []Event{{Seq: uint64(i + 1), Key: key, Time: at(sec), Match: true}})
	}
	if d := e.Drain(); len(d) != 1 || d[0].Edge == EdgeResolved || !d[0].At.Equal(at(102)) {
		t.Fatalf("in-order 3-in-10s should raise exactly once at 102, got %+v", d)
	}
	if n := e.DrainLateSamples(); n != 0 {
		t.Errorf("in-order traffic must count no late samples, got %d", n)
	}
}

// The cutoff now reads the frontier, which is snapshotted state — so a restored engine must reach
// the same verdict on the same late sample as one that never restarted. This is what makes the
// clamp replay-correct rather than merely correct: if the frontier were NOT restored, a replay
// would start at a zero watermark, the clamp would fall back to the sample's own time, and the
// false raise would reappear only after a restart.
func TestLateSampleVerdictSurvivesRestore(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}
	key := SeriesKey{Rule: "r", Series: "dev"}
	live := NewEngine(rules, 5*time.Second)
	live.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
	live.ProcessResolved(2, at(105), []Event{{Seq: 2, Key: key, Time: at(105), Match: true}})
	live.Drain()

	data, err := live.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored, err := Restore(rules, 5*time.Second, data)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	late := []Event{{Seq: 3, Key: key, Time: at(40), Match: true}}
	live.ProcessResolved(3, at(40), late)
	restored.ProcessResolved(3, at(40), late)
	if l, r := live.Drain(), restored.Drain(); len(l) != len(r) {
		t.Fatalf("restored engine diverged on the late sample: live %+v, restored %+v", l, r)
	} else if len(l) != 0 {
		t.Fatalf("neither engine should have fired, got %+v", l)
	}
	if l, r := live.DrainLateSamples(), restored.DrainLateSamples(); l != r || l != 1 {
		t.Errorf("late count: live %d, restored %d, want 1 each", l, r)
	}
}

// A NON-matching sample folds nothing in either, but it is not late — it still ages the window,
// which is how a filtering rule observes its falling edge. Counting it would make the metric read
// as sample loss on every rule with a `when` leaf that is currently false.
func TestNonMatchingSampleIsNotCountedLate(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, 5*time.Second)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
	e.ProcessResolved(2, at(101), []Event{{Seq: 2, Key: key, Time: at(101), Match: false}})
	e.Drain()
	if n := e.DrainLateSamples(); n != 0 {
		t.Errorf("a current non-matching sample is not late, got %d", n)
	}
}

// DrainLateSamples is drain-and-reset, like Drain: the caller adds what it gets to a counter
// without tracking a delta, so a second call with nothing in between must report zero rather
// than re-reporting the same samples.
func TestDrainLateSamplesResets(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Repeating, Window: 10 * time.Second, Count: 3}}, 5*time.Second)
	key := SeriesKey{Rule: "r", Series: "dev"}
	e.ProcessResolved(1, at(100), []Event{{Seq: 1, Key: key, Time: at(100), Match: true}})
	e.ProcessResolved(2, at(40), []Event{{Seq: 2, Key: key, Time: at(40), Match: true}})
	if n := e.DrainLateSamples(); n != 1 {
		t.Fatalf("first drain: got %d, want 1", n)
	}
	if n := e.DrainLateSamples(); n != 0 {
		t.Errorf("second drain must reset to 0, got %d", n)
	}
}
