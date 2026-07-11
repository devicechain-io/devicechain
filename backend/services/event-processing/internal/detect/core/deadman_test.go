// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sort"
	"testing"
	"time"
)

// absKey is the dead-man series key for a device under an absence rule.
func absKey(rule, device string) SeriesKey { return SeriesKey{Rule: rule, Series: device} }

// deadmanEngine is one absence rule (10s timeout) on a fresh engine — the substrate for the
// dead-man arming tests. The engine has never seen an event, so nothing is armed until
// SetExpected runs.
func deadmanEngine() *Engine {
	return NewEngine([]Rule{{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second}}, 0)
}

// TestSetExpectedFiresNeverSeenDevice: a device that never reported still fires absence — the
// whole point of the dead-man. Armed at since=0, its deadline is since+Timeout=10; advancing
// the watermark past it (as idle-advance does on a silent stream) fires it once, stamped at the
// grace deadline, not the overshoot.
func TestSetExpectedFiresNeverSeenDevice(t *testing.T) {
	e := deadmanEngine()
	e.SetExpected(absKey("rAbs", "dev"), at(0))
	e.Advance(at(11))
	got := e.Drain()
	if len(got) != 1 || got[0].RuleID != "rAbs" || got[0].Series != "dev" || got[0].Kind != Absence {
		t.Fatalf("dead-man did not fire for a never-seen device: %+v", got)
	}
	if !got[0].At.Equal(at(10)) {
		t.Fatalf("dead-man stamped at %v, want the grace deadline %v", got[0].At, at(10))
	}
}

// TestSetExpectedOnceSemantics: after a dead-man fires, re-running SetExpected (as restart
// reconciliation does for every still-expected series) must NOT re-arm it — the load-bearing
// once-semantics that keeps a never-seen device from firing a second absence on every restart.
func TestSetExpectedOnceSemantics(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(0))
	e.Advance(at(11))
	if got := e.Drain(); len(got) != 1 {
		t.Fatalf("expected the dead-man to fire once, got %v", got)
	}
	// Reconciliation re-arms with the SAME grace base: the fired latch must swallow it.
	e.SetExpected(key, at(0))
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("re-arming a fired dead-man double-fired it: %v", got)
	}
}

// TestSetExpectedLaterBaseReArmsAfterFire is the epoch case: after a dead-man fires, a re-publish
// or rollback (device-management stamps a fresh, LATER publishedAt to grant a fresh grace window)
// re-arms the dead-man rather than being swallowed by the fired latch. A same-or-earlier base
// after firing stays suppressed (the once-semantics that keeps restart reconciliation from
// double-firing at the elapsed base).
func TestSetExpectedLaterBaseReArmsAfterFire(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(0)) // deadline 10
	e.Advance(at(11))
	if got := e.Drain(); len(got) != 1 || !got[0].At.Equal(at(10)) {
		t.Fatalf("setup: dead-man did not fire at 10: %+v", got)
	}
	// Same base again (a reconcile duplicate of the fired epoch): suppressed.
	e.SetExpected(key, at(0))
	e.Advance(at(50))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("a same-base re-arm after firing double-fired: %v", got)
	}
	// A strictly-later base (re-publish/rollback fresh grace window): re-arms at 100+10=110.
	e.SetExpected(key, at(100))
	e.Advance(at(109))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("re-armed dead-man fired before its fresh deadline 110: %v", got)
	}
	e.Advance(at(111))
	if got := e.Drain(); len(got) != 1 || !got[0].At.Equal(at(110)) {
		t.Fatalf("a re-publish with a later grace base did not re-arm the dead-man: %+v", got)
	}
}

// TestSetExpectedLaterBaseReArmsAcrossRestart proves the epoch re-arm survives a snapshot: a fired
// dead-man is latched in the snapshot, and after Restore a re-publish with a later base re-arms it
// while a same-base reconcile does not.
func TestSetExpectedLaterBaseReArmsAcrossRestart(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(0))
	e.Advance(at(11)) // fires at 10, latches done at since=0
	e.Drain()

	rules := []Rule{{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second}}
	re, err := Restore(rules, 0, []byte(snapString(t, e)))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	re.SetExpected(key, at(0)) // reconcile at the fired base: suppressed
	re.Advance(at(50))
	if got := re.Drain(); len(got) != 0 {
		t.Fatalf("post-restart same-base reconcile double-fired: %v", got)
	}
	re.SetExpected(key, at(100)) // re-publish after restart: re-arms
	re.Advance(at(111))
	if got := re.Drain(); len(got) != 1 || !got[0].At.Equal(at(110)) {
		t.Fatalf("post-restart re-publish did not re-arm the dead-man: %+v", got)
	}
}

// TestSetExpectedIgnoresNonAbsenceAndUnknownRule: only a KNOWN absence rule has a dead-man.
// SetExpected against a non-absence rule or an unregistered id is a clean no-op — it never
// fabricates a timer or an expected entry to fire spuriously.
func TestSetExpectedIgnoresNonAbsenceAndUnknownRule(t *testing.T) {
	e := NewEngine([]Rule{
		{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second},
		{ID: "rDur", Kind: Duration, Hold: 10 * time.Second},
	}, 0)
	e.SetExpected(absKey("rDur", "dev"), at(0))     // duration is not a dead-man
	e.SetExpected(absKey("rUnknown", "dev"), at(0)) // no such rule
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("SetExpected fabricated a firing for a non-absence/unknown rule: %v", got)
	}
	if keys := e.ExpectedKeys(); len(keys) != 0 {
		t.Fatalf("SetExpected recorded an entry for a non-absence/unknown rule: %v", keys)
	}
}

// TestSetExpectedForwardOnlyBase: a later grace base (a re-publish) pushes the deadline OUT, but
// an earlier one never pulls it in — the forward-only discipline that keeps a stale/late arming
// from prematurely firing a dead-man a later arming already extended.
func TestSetExpectedForwardOnlyBase(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(50)) // deadline 60
	e.SetExpected(key, at(10)) // earlier base: must NOT shrink the deadline to 20
	e.Advance(at(25))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("an earlier grace base shrank the dead-man deadline: %v", got)
	}
	e.Advance(at(61))
	if got := e.Drain(); len(got) != 1 || !got[0].At.Equal(at(60)) {
		t.Fatalf("dead-man did not fire at the forward-only deadline 60: %+v", got)
	}
}

// TestSetExpectedSupersededByHeartbeat: once a dead-man-armed device finally reports, its
// heartbeat re-arms the absence timer forward off the event time — so the dead-man never fires
// for a device that came to life within its grace window.
func TestSetExpectedSupersededByHeartbeat(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	e.SetExpected(key, at(0)) // dead-man deadline 10
	// The device reports at 8 (before the dead-man deadline): heartbeat arms 8+10=18.
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(8), Match: true})
	e.Advance(at(11)) // past the dead-man deadline 10, but the heartbeat pushed it to 18
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("dead-man fired for a device that reported within its grace window: %v", got)
	}
	e.Advance(at(19)) // past the heartbeat deadline 18: a normal absence fires
	if got := e.Drain(); len(got) != 1 || !got[0].At.Equal(at(18)) {
		t.Fatalf("normal absence did not fire at the heartbeat deadline 18: %+v", got)
	}
}

// TestRemoveExpectedCancelsDeadManAndTimer: RemoveExpected (a deleted / re-typed device) drops
// the entry AND cancels the wheel timer, so neither an unfired dead-man nor a heartbeat-armed
// absence fires for a departed device.
func TestRemoveExpectedCancelsDeadManAndTimer(t *testing.T) {
	e := deadmanEngine()
	key := absKey("rAbs", "dev")
	// A device that reported (heartbeat-armed at 18) and was ALSO dead-man armed.
	e.SetExpected(key, at(0))
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: at(8), Match: true})
	e.RemoveExpected(key)
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("a removed device still fired absence: %v", got)
	}
	if keys := e.ExpectedKeys(); len(keys) != 0 {
		t.Fatalf("RemoveExpected left an entry behind: %v", keys)
	}
}

// TestRemoveRuleGCsExpected: removing an absence rule (governance/teardown) sweeps its dead-man
// entries and timers along with the rest of its keyed state — no orphaned expected entry lingers.
func TestRemoveRuleGCsExpected(t *testing.T) {
	e := deadmanEngine()
	e.SetExpected(absKey("rAbs", "devA"), at(0))
	e.SetExpected(absKey("rAbs", "devB"), at(0))
	e.RemoveRule("rAbs")
	if keys := e.ExpectedKeys(); len(keys) != 0 {
		t.Fatalf("RemoveRule left dead-man entries behind: %v", keys)
	}
	e.Advance(at(100))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("a removed rule's dead-man still fired: %v", got)
	}
}

// TestExpectedSnapshotRoundTripPreservesOnceSemantics is the crux restart test: a snapshot
// captures both an UNFIRED dead-man (devA, timer still live) and a FIRED one (devB, latched
// done with no live timer). After Restore + reconciliation (SetExpected for both, as slice
// 4c-2b-2b does), devA still fires exactly once and devB — already fired — does NOT fire again.
func TestExpectedSnapshotRoundTripPreservesOnceSemantics(t *testing.T) {
	e := deadmanEngine()
	keyA, keyB := absKey("rAbs", "devA"), absKey("rAbs", "devB")
	e.SetExpected(keyA, at(100)) // deadline 110 — unfired at snapshot
	e.SetExpected(keyB, at(0))   // deadline 10
	e.Advance(at(11))            // fires devB (10), not devA (110)
	if got := e.Drain(); len(got) != 1 || got[0].Series != "devB" {
		t.Fatalf("setup: expected only devB to fire pre-snapshot, got %+v", got)
	}

	data := snapString(t, e)
	rules := []Rule{{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second}}
	re, err := Restore(rules, 0, []byte(data))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if keys := re.ExpectedKeys(); len(keys) != 2 {
		t.Fatalf("restore lost expected entries: %v", keys)
	}

	// Reconciliation re-arms every still-expected series with its recomputed grace base.
	re.SetExpected(keyA, at(100))
	re.SetExpected(keyB, at(0))
	re.Advance(at(111))
	got := re.Drain()
	if len(got) != 1 || got[0].Series != "devA" || !got[0].At.Equal(at(110)) {
		t.Fatalf("after restart, want exactly devA firing once at 110 (devB already fired), got %+v", got)
	}
}

// TestSetExpectedPastDeadlineFiresOnNextAdvance: arming a series whose grace already elapsed (a
// device rostered long ago, absence rule just published against it) does not fire inside
// SetExpected — it schedules, and the next watermark advance fires it.
func TestSetExpectedPastDeadlineFiresOnNextAdvance(t *testing.T) {
	e := deadmanEngine()
	e.Advance(at(100)) // watermark already well past a since=0 deadline
	e.SetExpected(absKey("rAbs", "dev"), at(0))
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("SetExpected fired synchronously; it must only schedule: %v", got)
	}
	e.Advance(at(101))
	if got := e.Drain(); len(got) != 1 || !got[0].At.Equal(at(10)) {
		t.Fatalf("an overdue dead-man did not fire on the next advance: %+v", got)
	}
}

// TestExpectedKeysReportsArmedSet confirms ExpectedKeys enumerates exactly the armed series
// (the membership set slice 4c-2b-2b's reconciliation diffs against the read-models).
func TestExpectedKeysReportsArmedSet(t *testing.T) {
	e := deadmanEngine()
	e.SetExpected(absKey("rAbs", "devA"), at(0))
	e.SetExpected(absKey("rAbs", "devB"), at(0))
	got := e.ExpectedKeys()
	sort.Slice(got, func(i, j int) bool { return got[i].Series < got[j].Series })
	want := []SeriesKey{absKey("rAbs", "devA"), absKey("rAbs", "devB")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ExpectedKeys = %v, want %v", got, want)
	}
}

// TestHeartbeatAbsenceKeysExcludesDeadManAndNonAbsence: HeartbeatAbsenceKeys returns a series with a
// heartbeat-armed absence timer (apply, Absence) but NOT one that also has a dead-man expected entry
// (that is the ExpectedKeys sweep's job) NOR a non-absence rule's timer.
func TestHeartbeatAbsenceKeysExcludesDeadManAndNonAbsence(t *testing.T) {
	e := NewEngine([]Rule{
		{ID: "rAbs", Kind: Absence, Timeout: 10 * time.Second},
		{ID: "rDur", Kind: Duration, Hold: 10 * time.Second},
	}, 0)
	// heartbeat-only absence timer (reported then silent, no dead-man entry)
	e.ProcessEvent(Event{Seq: 1, Key: absKey("rAbs", "hb"), Time: at(1), Match: true})
	// dead-man-armed absence timer (in e.expected — owned by the ExpectedKeys sweep)
	e.SetExpected(absKey("rAbs", "dm"), at(0))
	// a Duration timer — a wheel timer that is NOT an absence
	e.ProcessEvent(Event{Seq: 2, Key: absKey("rDur", "dur"), Time: at(1), Match: true})

	got := e.HeartbeatAbsenceKeys()
	if len(got) != 1 || got[0] != absKey("rAbs", "hb") {
		t.Fatalf("HeartbeatAbsenceKeys = %v, want exactly [rAbs/hb]", got)
	}
}
