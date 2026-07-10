// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"
	"time"
)

// statefulKinds returns one rule of every state-carrying kind, id-prefixed so two disjoint
// rule sets (e.g. "A"/"B") can run in one engine. Windows/holds are long (100s) so the
// accumulation phase leaves live state without firing, giving RemoveRule something to sweep.
func statefulKinds(prefix string) []Rule {
	return []Rule{
		{ID: prefix + "Dur", Kind: Duration, Hold: 100 * time.Second},
		{ID: prefix + "Abs", Kind: Absence, Timeout: 100 * time.Second},
		{ID: prefix + "Rep", Kind: Repeating, Window: 100 * time.Second, Count: 3},
		{ID: prefix + "Agg", Kind: Aggregate, Window: 100 * time.Second, Agg: AggAvg, Op: GT, Thresh: 50},
		{ID: prefix + "Delta", Kind: DeltaRate, Op: GT, Thresh: 5},
		{ID: prefix + "Count", Kind: CountWindow, Count: 3, Agg: AggSum, Op: GT, Thresh: 5},
		{ID: prefix + "Sess", Kind: Session, Gap: 100 * time.Second, Agg: AggCount, Op: GE, Thresh: 3},
		{ID: prefix + "Slide", Kind: SlidingAgg, Window: 100 * time.Second, Agg: AggMax, Op: GT, Thresh: 50},
		{ID: prefix + "Corr", Kind: Correlation, Window: 100 * time.Second, Count: 3, MemberCap: 100},
	}
}

// accumulate drives every kind of a prefix partway toward firing (open pane, primed delta,
// partial count, armed timer, one distinct member, …) WITHOUT firing anything, so the engine
// holds live state for the prefix across every keyed map, the timer wheel, and the close heap.
func accumulate(prefix string) []step {
	dev, area := prefix+"dev", prefix+"area"
	return []step{
		evStep(0, prefix+"Dur", dev, 1, true),       // active since 1 (hold 100)
		evStep(0, prefix+"Abs", dev, 1, true),       // dead-man deadline 101
		evStep(0, prefix+"Rep", dev, 1, true),       // count 1 < 3
		evValStep(0, prefix+"Agg", dev, 1, 60),      // open pane [0,100)
		evValStep(0, prefix+"Delta", dev, 1, 10),    // primed, no delta yet
		evValStep(0, prefix+"Count", dev, 1, 3),     // count 1 < 3
		evValStep(0, prefix+"Sess", dev, 1, 1),      // session {1}, gap deadline 101
		evValStep(0, prefix+"Slide", dev, 1, 40),    // max 40 < 50, no fire
		evCorrStep(0, prefix+"Corr", area, "m1", 1), // distinct 1 < 3
	}
}

// fireAll drives every kind of a prefix to its firing point, given the accumulate state.
// Yields, per prefix: Rep@3, Delta@4, Count@6, Slide@7, Corr@9, then on the advance to 102:
// Agg@100 (pane closes, avg 60>50) and Dur@101 / Abs@101 (deadlines elapse). Session closes
// with count 1 (< 3) and does NOT fire — its state is still swept on removal.
func fireAll(prefix string) []step {
	dev, area := prefix+"dev", prefix+"area"
	return []step{
		evStep(0, prefix+"Rep", dev, 2, true),
		evStep(0, prefix+"Rep", dev, 3, true),     // count 3 -> FIRE Rep@3
		evValStep(0, prefix+"Delta", dev, 4, 100), // +90 -> FIRE Delta@4
		evValStep(0, prefix+"Count", dev, 5, 4),
		evValStep(0, prefix+"Count", dev, 6, 4),  // count 3, sum 11>5 -> FIRE Count@6
		evValStep(0, prefix+"Slide", dev, 7, 60), // max 60>50 -> FIRE Slide@7
		evCorrStep(0, prefix+"Corr", area, "m2", 8),
		evCorrStep(0, prefix+"Corr", area, "m3", 9), // distinct 3 -> FIRE Corr@9
		advStep(102), // closes Agg@100 + Dur@101 + Abs@101
	}
}

// feedSeq applies steps with monotonically-assigned sequences (so interleaving two prefixes'
// events in one engine never trips the message-level idempotency guard) and returns every
// detection drained along the way.
func feedSeq(e *Engine, seq *uint64, steps []step) []Detection {
	var all []Detection
	for _, s := range steps {
		if s.ev != nil {
			*seq++
			ev := *s.ev
			ev.Seq = *seq
			e.ProcessEvent(ev)
		} else {
			e.Advance(*s.adv)
		}
		all = append(all, e.Drain()...)
	}
	return all
}

func snapString(t *testing.T, e *Engine) string {
	t.Helper()
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return string(b)
}

// TestUpsertRuleAddsAndDetectsForward: a rule added to a running engine takes effect
// immediately and detects forward from activation — a brand-new id starts with empty state.
func TestUpsertRuleAddsAndDetectsForward(t *testing.T) {
	e := NewEngine(nil, 0)
	var seq uint64
	// The engine is already past t=50 with no rules.
	e.Advance(at(50))
	if got := feedSeq(e, &seq, []step{evStep(0, "rThr", "dev", 51, true)}); len(got) != 0 {
		t.Fatalf("expected no detection before the rule exists, got %v", got)
	}
	e.UpsertRule(Rule{ID: "rThr", Kind: Threshold})
	got := feedSeq(e, &seq, []step{evStep(0, "rThr", "dev", 52, true)})
	if len(got) != 1 || got[0].RuleID != "rThr" || !got[0].At.Equal(at(52)) {
		t.Fatalf("added threshold rule did not fire forward: %+v", got)
	}
}

// TestUpsertRulePreservesRunningState: re-upserting an already-running rule (a redelivered
// published-rule fact) installs the byte-identical rule WITHOUT resetting its live state — the
// duration hold in flight still matures and fires.
func TestUpsertRulePreservesRunningState(t *testing.T) {
	r := Rule{ID: "rDur", Kind: Duration, Hold: 10 * time.Second}
	e := NewEngine([]Rule{r}, 0)
	var seq uint64
	feedSeq(e, &seq, []step{evStep(0, "rDur", "dev", 0, true)}) // active since 0, deadline 10
	e.UpsertRule(r)                                             // redelivered fact: must not reset the hold
	got := feedSeq(e, &seq, []step{advStep(11)})                // deadline 10 elapses
	if len(got) != 1 || got[0].RuleID != "rDur" || !got[0].At.Equal(at(10)) {
		t.Fatalf("re-upsert reset the running duration hold: %+v", got)
	}
}

// TestUpsertRuleReusedIdDifferentBodyResetsState: when an existing id arrives with a DIFFERENT
// rule body (a deleted-and-reused profile token re-minting an old id with new semantics), the
// old rule's keyed state must be GC'd — not grafted onto the new rule. An in-flight Duration
// hold under id "r" must not survive "r" becoming a Threshold.
func TestUpsertRuleReusedIdDifferentBodyResetsState(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Duration, Hold: 10 * time.Second}}, 0)
	var seq uint64
	feedSeq(e, &seq, []step{evStep(0, "r", "dev", 0, true)}) // duration active since 0, deadline 10
	e.UpsertRule(Rule{ID: "r", Kind: Threshold})             // reused id, different body: reset state
	if got := feedSeq(e, &seq, []step{advStep(20)}); len(got) != 0 {
		t.Fatalf("stale duration hold survived a changed-body upsert: %+v", got)
	}
	got := feedSeq(e, &seq, []step{evStep(0, "r", "dev", 21, true)})
	if len(got) != 1 || got[0].Kind != Threshold {
		t.Fatalf("reused-id threshold rule did not fire clean: %+v", got)
	}
}

// TestRemoveRuleStopsFiring: a removed rule no longer fires on a matching event.
func TestRemoveRuleStopsFiring(t *testing.T) {
	e := NewEngine([]Rule{{ID: "rThr", Kind: Threshold}}, 0)
	var seq uint64
	e.RemoveRule("rThr")
	if got := feedSeq(e, &seq, []step{evStep(0, "rThr", "dev", 1, true)}); len(got) != 0 {
		t.Fatalf("removed rule still fired: %v", got)
	}
}

// TestRemoveRuleGCsStateAndSpareOthers is the load-bearing removal test: with two full rule
// sets ("A" and "B") holding live state across every keyed map, the timer wheel, and the close
// heap, removing all of "A" (a) leaves NO trace of "A" in a snapshot and (b) leaves "B"'s state
// perfectly intact — "B" fires exactly as it would in an engine that never held "A" at all. A
// full-rebuild removal would instead wipe "B"'s windows/timers; this proves the surgical GC.
func TestRemoveRuleGCsStateAndSpareOthers(t *testing.T) {
	rulesA, rulesB := statefulKinds("A"), statefulKinds("B")
	eAB := NewEngine(append(append([]Rule{}, rulesA...), rulesB...), 0)
	var seqAB uint64
	feedSeq(eAB, &seqAB, accumulate("A"))
	feedSeq(eAB, &seqAB, accumulate("B"))

	before := snapString(t, eAB)
	for _, r := range rulesA {
		if !strings.Contains(before, r.ID) {
			t.Fatalf("pre-removal snapshot missing expected live state for %q", r.ID)
		}
	}

	for _, r := range rulesA {
		eAB.RemoveRule(r.ID)
	}

	// (a) No "A" state survives the sweep — every keyed map, the wheel, and the close heap.
	after := snapString(t, eAB)
	for _, r := range rulesA {
		if strings.Contains(after, r.ID) {
			t.Fatalf("post-removal snapshot still references removed rule %q: %s", r.ID, after)
		}
	}
	if strings.Contains(after, "adev") || strings.Contains(after, "aarea") {
		t.Fatalf("post-removal snapshot still references removed rule's series: %s", after)
	}
	// (b) "B" state is untouched — a "B" id must still be present.
	if !strings.Contains(after, "BDur") {
		t.Fatalf("post-removal snapshot lost surviving rule state: %s", after)
	}

	// (c) "B" fires identically to an engine that only ever held "B".
	gotAB := feedSeq(eAB, &seqAB, fireAll("B"))

	eB := NewEngine(rulesB, 0)
	var seqB uint64
	feedSeq(eB, &seqB, accumulate("B"))
	gotB := feedSeq(eB, &seqB, fireAll("B"))

	assertSetEqual(t, dedup(gotB), dedup(gotAB))
	if len(gotB) == 0 {
		t.Fatal("test bug: the surviving rule set fired nothing to compare")
	}
}

// TestRemoveRuleSnapshotRestoreRoundTrips: after a removal, the swept snapshot restores (with
// the reduced rule set) to a byte-identical snapshot — the post-GC state is fully serializable
// and carries nothing for the removed rule.
func TestRemoveRuleSnapshotRestoreRoundTrips(t *testing.T) {
	rulesA, rulesB := statefulKinds("A"), statefulKinds("B")
	e := NewEngine(append(append([]Rule{}, rulesA...), rulesB...), 0)
	var seq uint64
	feedSeq(e, &seq, accumulate("A"))
	feedSeq(e, &seq, accumulate("B"))
	for _, r := range rulesA {
		e.RemoveRule(r.ID)
	}
	snap := snapString(t, e)

	restored, err := Restore(rulesB, 0, []byte(snap))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := snapString(t, restored); got != snap {
		t.Fatalf("snapshot not stable across restore after removal:\n before=%s\n after =%s", snap, got)
	}
}
