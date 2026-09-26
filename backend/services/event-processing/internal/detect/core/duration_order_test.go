// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// A Duration rule's run is ordered by EVENT time, not arrival order: a late reading is placed where
// it happened, and a reading further behind the frontier than the rule's hold is refused and counted
// (applyDuration). Every test here asserts the exact detections (rule, series, kind, edge, instant),
// never a count of them.

// durEngine builds an engine holding one Duration rule "r".
func durEngine(lateness, hold time.Duration) *Engine {
	return NewEngine([]Rule{{ID: "r", Kind: Duration, Hold: hold}}, lateness)
}

// send feeds one reading for (r, series) as its own message, stamped at the reading's own time,
// and returns what it emitted.
func send(e *Engine, seq uint64, series string, sec int, match bool) []Detection {
	e.ProcessResolved(seq, at(sec), []Event{{Seq: seq, Key: SeriesKey{Rule: "r", Series: series}, Time: at(sec), Match: match}})
	return e.Drain()
}

// advanceTo moves the frontier with no event and returns what it emitted.
func advanceTo(e *Engine, sec int) []Detection {
	e.Advance(at(sec))
	return e.Drain()
}

func raisedAt(series string, sec int) Detection {
	return Detection{RuleID: "r", Series: series, Kind: Duration, Edge: EdgeRaised, At: at(sec)}
}

func resolvedAt(series string, sec int) Detection {
	return Detection{RuleID: "r", Series: series, Kind: Duration, Edge: EdgeResolved, At: at(sec)}
}

// expectDetections compares whole slices; nil and empty are the same answer ("nothing").
func expectDetections(t *testing.T, step string, got []Detection, want ...Detection) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %+v, want %+v", step, got, want)
	}
}

func expectLate(t *testing.T, e *Engine, want uint64) {
	t.Helper()
	if got := e.DrainLateSamples(); got != want {
		t.Errorf("late samples = %d, want %d", got, want)
	}
}

// A late non-matching reading OLDER than the run it arrives in says nothing about the run: the
// condition began after it. It used to cancel the hold outright.
func TestDurationStaleNonMatchDoesNotCancelTheHold(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	expectDetections(t, "m@100", send(e, 1, "d", 100, true))
	expectDetections(t, "m@104", send(e, 2, "d", 104, true))
	expectDetections(t, "late non@95", send(e, 3, "d", 95, false))
	expectDetections(t, "frontier 109", advanceTo(e, 139))
	expectDetections(t, "frontier 110", advanceTo(e, 140), raisedAt("d", 110))
	expectLate(t, e, 0)
}

// A late MATCHING reading older than a break the engine has already seen must not open a run across
// that break. It used to, and raised an alarm the readings contradict.
func TestDurationLateMatchCannotReopenAcrossABreak(t *testing.T) {
	t.Run("before the break", func(t *testing.T) {
		e := durEngine(30*time.Second, 10*time.Second)
		send(e, 1, "d", 0, true)
		send(e, 2, "d", 5, false)
		expectDetections(t, "late m@3", send(e, 3, "d", 3, true))
		expectDetections(t, "frontier 20", advanceTo(e, 50))
		// Positive control: the refusal is specific to the crossing match, not a blanket one.
		expectDetections(t, "m@61", send(e, 4, "d", 61, true))
		expectDetections(t, "frontier 71", advanceTo(e, 101), raisedAt("d", 71))
	})
	t.Run("tie", func(t *testing.T) {
		e := durEngine(30*time.Second, 10*time.Second)
		send(e, 1, "d", 0, true)
		send(e, 2, "d", 5, false)
		expectDetections(t, "late m@5", send(e, 3, "d", 5, true))
		expectDetections(t, "frontier 20", advanceTo(e, 50))
	})
}

// A late non-match INSIDE a live run is a known break, and the newest match shows the condition
// resumed: the run restarts at that newest match. It used to end the run.
func TestDurationLateBreakInsideARunRestartsIt(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	send(e, 2, "d", 10, true)
	expectDetections(t, "late non@5", send(e, 3, "d", 5, false))
	expectDetections(t, "frontier 19", advanceTo(e, 49))
	expectDetections(t, "frontier 20", advanceTo(e, 50), raisedAt("d", 20))
}

// A late match older than the run's newest must not pull the restart point backwards.
func TestDurationStaleMatchDoesNotRegressTheRestartPoint(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	send(e, 2, "d", 10, true)
	send(e, 3, "d", 4, true)
	send(e, 4, "d", 6, false)
	expectDetections(t, "frontier 20", advanceTo(e, 50), raisedAt("d", 20))
}

// A non-match at or after the newest match ends the run; a break wins the tie.
func TestDurationBreakAtOrAfterTheNewestMatchEndsTheRun(t *testing.T) {
	for _, brk := range []int{6, 4} {
		e := durEngine(30*time.Second, 10*time.Second)
		send(e, 1, "d", 0, true)
		send(e, 2, "d", 4, true)
		send(e, 3, "d", brk, false)
		expectDetections(t, "frontier 70", advanceTo(e, 100))
	}
}

// A reading further behind the frontier than the hold can only speak to a hold the frontier has
// already decided, so a match that far back is refused and counted. A non-matching reading that far
// back is USED when it ends a raised alarm (the falling edge is still the current word) and ignored
// otherwise, where it can change nothing.
func TestDurationRefusesAndCountsSamplesPastItsHold(t *testing.T) {
	e := durEngine(5*time.Second, 10*time.Second)
	expectDetections(t, "d m@100", send(e, 1, "d", 100, true))
	// Another device's reading moves the frontier to 195.
	expectDetections(t, "other non@200", send(e, 2, "other", 200, false), raisedAt("d", 110))

	expectDetections(t, "d non@150 (past the hold, ends the raise)", send(e, 3, "d", 150, false), resolvedAt("d", 150))
	expectDetections(t, "e m@150 (refused)", send(e, 4, "e", 150, true))
	expectDetections(t, "f m@185 (refused: 185+10 is the frontier)", send(e, 5, "f", 185, true))
	expectDetections(t, "h non@150 (past the hold, nothing raised)", send(e, 6, "h", 150, false))
	expectDetections(t, "g m@186 (admitted)", send(e, 7, "g", 186, true))
	expectDetections(t, "d non@196", send(e, 8, "d", 196, false))
	expectDetections(t, "frontier 295", advanceTo(e, 300), raisedAt("g", 196))
	expectLate(t, e, 2) // e@150 and f@185; the non-matches are not refused matches
}

// A raised alarm must still be ended by a non-matching reading that arrives further behind the
// frontier than the hold: a device whose readings always arrive that late could otherwise never
// clear it. The reading is used, so it is not counted.
func TestDurationFallingEdgePastTheHoldStillResolves(t *testing.T) {
	e := durEngine(5*time.Second, 10*time.Second)
	send(e, 1, "d", 100, true)
	expectDetections(t, "frontier 195", send(e, 2, "other", 200, false), raisedAt("d", 110))
	expectDetections(t, "d non@150", send(e, 3, "d", 150, false), resolvedAt("d", 150))
	expectLate(t, e, 0)
	// The run went with the alarm: a later match opens a fresh one and raises a full hold after it.
	expectDetections(t, "d m@190", send(e, 4, "d", 190, true))
	expectDetections(t, "frontier 200", advanceTo(e, 205), raisedAt("d", 200))
}

// A store-and-forward batch is used as far back as the hold reaches, not only from the frontier on.
// A strict "behind the frontier is late" rule would open the run at 95 and raise nothing by 96.
func TestDurationUsesABatchInsideItsHold(t *testing.T) {
	e := durEngine(5*time.Second, 60*time.Second)
	var evs []Event
	for s := 30; s <= 100; s++ {
		evs = append(evs, Event{Seq: 1, Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(s), Match: true})
	}
	e.ProcessResolved(1, at(100), evs)
	expectDetections(t, "batch", e.Drain())
	expectDetections(t, "frontier 96", advanceTo(e, 101), raisedAt("d", 96))
	expectLate(t, e, 6) // 30..35: each is at least a hold behind the frontier of 95
}

// The kept break follows the NEWEST break: its expiry re-arms from there, so a late match older than
// that break is still refused after the first break's hold has passed.
func TestDurationKeptBreakFollowsTheNewestBreak(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, false)
	send(e, 2, "d", 8, false)
	expectDetections(t, "frontier 10", advanceTo(e, 40))
	expectDetections(t, "late m@7", send(e, 3, "d", 7, true))
	expectDetections(t, "frontier 70", advanceTo(e, 100))
}

// An older break arriving late must not rewind the kept break.
func TestDurationOlderBreakDoesNotRewindTheKeptBreak(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, false)
	send(e, 2, "d", 8, false)
	send(e, 3, "d", 5, false)
	send(e, 4, "d", 6, true)
	expectDetections(t, "frontier 70", advanceTo(e, 100))
}

// The kept break is real state: it and its expiry timer count as live keys, and both are released
// once the frontier passes the break by a hold.
func TestDurationKeptBreakIsCountedAndReleased(t *testing.T) {
	e := durEngine(0, 10*time.Second)
	send(e, 1, "d", 0, false)
	if got := e.LiveKeyCounts()["r"]; got != 2 {
		t.Fatalf("a kept break holds its record and its expiry timer: live keys = %d, want 2", got)
	}
	advanceTo(e, 10)
	if got := e.LiveKeyCounts()["r"]; got != 0 {
		t.Fatalf("the kept break outlived its hold: live keys = %d, want 0", got)
	}
	if e.HasPendingWork() {
		t.Fatal("a released kept break left pending timer work")
	}
}

// The run is kept after the raise, so a late break inside it restarts it at the newest match, which
// raises again a hold later if the condition still holds.
func TestDurationBreakAfterTheRaiseRestartsAtTheNewestMatch(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	expectDetections(t, "m@45", send(e, 2, "d", 45, true), raisedAt("d", 10))
	expectDetections(t, "late non@20", send(e, 3, "d", 20, false), resolvedAt("d", 20))
	expectDetections(t, "frontier 54", advanceTo(e, 84))
	expectDetections(t, "frontier 55", advanceTo(e, 85), raisedAt("d", 55))
}

// A break older than the raise arrives after the hold was decided: it cannot withdraw the alarm, and
// it is counted as a late sample so the contradiction is visible. A break at or after the raise is
// still the falling edge.
func TestDurationBreakOlderThanTheRaiseIsIgnored(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	expectDetections(t, "m@45", send(e, 2, "d", 45, true), raisedAt("d", 10))
	expectDetections(t, "late non@8", send(e, 3, "d", 8, false))
	expectLate(t, e, 1)
	expectDetections(t, "late non@30", send(e, 4, "d", 30, false), resolvedAt("d", 30))
	expectDetections(t, "frontier 55", advanceTo(e, 85), raisedAt("d", 55))
	expectLate(t, e, 0)
}

// The raise and the break that contradicts it can arrive in the same message: the message's
// envelope moves the frontier past the hold before its readings are placed. The break is then older
// than the raise, which stands, and the break is counted.
func TestDurationBreakArrivingBehindItsRaiseIsCountedLate(t *testing.T) {
	e := durEngine(5*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	e.ProcessResolved(2, at(16), []Event{{Seq: 2, Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(5), Match: false}})
	expectDetections(t, "batch at 16 carrying non@5", e.Drain(), raisedAt("d", 10))
	expectLate(t, e, 1)
}

// A known limit, pinned so it is a decision: the hold timer fires only once the frontier (the
// newest reading less the lateness tolerance) passes the deadline, so a run whose break arrives
// before then is ended without raising, even in order. An episode must last its hold plus the
// lateness tolerance to be certain to raise.
func TestDurationEpisodeShorterThanHoldPlusLatenessIsNotRaised(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	expectDetections(t, "non@12", send(e, 2, "d", 12, false))
	expectDetections(t, "frontier 70", advanceTo(e, 100))
}

// A checkpoint written before the run record existed ({rule, series, since}) restores as the open
// run it was: its hold still fires, and a later break still ends it.
func TestDurationRestoresACheckpointWrittenBeforeTheRunRecord(t *testing.T) {
	old := []byte(`{"watermark":"2026-01-01T00:00:00Z","lastSeq":1,` +
		`"active":[{"rule":"r","series":"d","since":"2026-01-01T00:00:00Z"}],` +
		`"timers":[{"deadline":"2026-01-01T00:00:10Z","rule":"r","series":"d","gen":1}],` +
		`"gens":[{"rule":"r","series":"d","gen":1}]}`)
	rules := []Rule{{ID: "r", Kind: Duration, Hold: 10 * time.Second}}

	e, err := Restore(rules, 0, old)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	expectDetections(t, "hold elapses", advanceTo(e, 10), raisedAt("d", 10))

	e, err = Restore(rules, 0, old)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	expectDetections(t, "non@5", send(e, 2, "d", 5, false))
	expectDetections(t, "frontier 20", advanceTo(e, 20))
}

// A checkpoint written before this change holds a raised duration alarm with NO run (the run used to
// be consumed at the raise). It restores with a run ending at the raise, so a late break that
// arrives after a newer match still ends the alarm instead of being taken for a reading older than a
// fresh run.
func TestDurationRestoresARaiseWrittenWithoutItsRun(t *testing.T) {
	old := []byte(`{"watermark":"2026-01-01T00:00:10Z","lastSeq":1,` +
		`"raised":[{"rule":"r","series":"d","at":"2026-01-01T00:00:10Z"}]}`)
	e, err := Restore([]Rule{{ID: "r", Kind: Duration, Hold: 10 * time.Second}}, 0, old)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	expectDetections(t, "m@20", send(e, 2, "d", 20, true))
	expectDetections(t, "late non@15", send(e, 3, "d", 15, false), resolvedAt("d", 15))
}

// A kept break is written under its own checkpoint key, never under "active". A binary from before
// this change reads every "active" entry as an open run and raises it when its timer fires, so a
// kept break written there would turn a rollback into a raise for every device that recently sent a
// non-matching reading.
func TestDurationKeptBreakIsNotWrittenAsAnOpenRun(t *testing.T) {
	e := durEngine(0, 10*time.Second)
	send(e, 1, "d", 0, false)
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var s struct {
		Active []json.RawMessage `json:"active"`
		Breaks []json.RawMessage `json:"durationBreaks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(s.Active) != 0 || len(s.Breaks) != 1 {
		t.Fatalf("active=%s durationBreaks=%s; want no open run and one kept break", s.Active, s.Breaks)
	}
	// And it round-trips: restored, the break still refuses a late match older than it.
	r, err := Restore([]Rule{{ID: "r", Kind: Duration, Hold: 10 * time.Second}}, 0, b)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	expectDetections(t, "late m@0", send(r, 2, "d", 0, true))
	expectDetections(t, "frontier 20", advanceTo(r, 20))
}

// ClearRaised (a raise the runtime terminally dropped) also drops the run kept after the raise, so
// the next match opens a fresh run and can raise again.
func TestClearRaisedLetsADurationRaiseAgain(t *testing.T) {
	e := durEngine(0, 10*time.Second)
	send(e, 1, "d", 0, true)
	expectDetections(t, "hold", advanceTo(e, 10), raisedAt("d", 10))
	e.ClearRaised(SeriesKey{Rule: "r", Series: "d"})
	expectDetections(t, "m@11", send(e, 2, "d", 11, true))
	expectDetections(t, "hold again", advanceTo(e, 21), raisedAt("d", 21))
}

// A late break that predates the run of a raised alarm contradicts nothing: it is ignored and NOT
// counted as late. Only a break inside the run it arrives behind is counted
// (TestDurationBreakOlderThanTheRaiseIsIgnored pins that side).
func TestDurationBreakOlderThanTheRaisedRunIsNotCounted(t *testing.T) {
	e := durEngine(30*time.Second, 10*time.Second)
	send(e, 1, "d", 20, true)
	expectDetections(t, "m@65", send(e, 2, "d", 65, true), raisedAt("d", 30))
	expectDetections(t, "late non@15", send(e, 3, "d", 15, false))
	expectLate(t, e, 0)
	// Positive control: a break inside the run, equally late, is counted.
	expectDetections(t, "late non@22", send(e, 4, "d", 22, false))
	expectLate(t, e, 1)
}

// ClearRaised leaves a KEPT BREAK alone. The raise and the break that ended its run can land in one
// drain: here a message stamped 16 moves the frontier to 11 (firing the hold at 10) and carries
// non@12. When the runtime then drops that raise, the break at 12 must survive, or a late m@11
// inside the budget would open a run across it and raise falsely at 21.
func TestClearRaisedKeepsABreakFromTheSameDrain(t *testing.T) {
	e := durEngine(5*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	e.ProcessResolved(2, at(16), []Event{{Seq: 2, Key: SeriesKey{Rule: "r", Series: "d"}, Time: at(12), Match: false}})
	expectDetections(t, "batch at 16 carrying non@12", e.Drain(), raisedAt("d", 10), resolvedAt("d", 12))
	e.ClearRaised(SeriesKey{Rule: "r", Series: "d"})
	expectDetections(t, "late m@11", send(e, 3, "d", 11, true))
	expectDetections(t, "frontier 30", advanceTo(e, 35))
	// Positive control: a match after the break still opens a run and raises.
	expectDetections(t, "m@36", send(e, 4, "d", 36, true))
	expectDetections(t, "frontier 46", advanceTo(e, 51), raisedAt("d", 46))
}

// ClearRaised leaves a RESTARTED run alone: its hold timer is still live, so it raises on its own.
// Here the raise at 10 and a restart at the newest match (13) land in one drain.
func TestClearRaisedKeepsARestartedRunsLiveHold(t *testing.T) {
	e := durEngine(5*time.Second, 10*time.Second)
	send(e, 1, "d", 0, true)
	k := SeriesKey{Rule: "r", Series: "d"}
	e.ProcessResolved(2, at(16), []Event{{Seq: 2, Key: k, Time: at(13), Match: true}, {Seq: 2, Key: k, Time: at(12), Match: false}})
	expectDetections(t, "batch at 16 carrying m@13, non@12", e.Drain(), raisedAt("d", 10), resolvedAt("d", 12))
	e.ClearRaised(k)
	expectDetections(t, "frontier 22", advanceTo(e, 27))
	expectDetections(t, "frontier 23", advanceTo(e, 28), raisedAt("d", 23))
}
