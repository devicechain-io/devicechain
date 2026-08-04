// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"
	"time"
)

// Tenant eviction (ADR-077 slice 2c) exercised through RemoveMatching.
//
// 🔑 THE ORACLE IS THE SNAPSHOT BYTES, and that is not a shortcut for lack of a better
// one — it is the strongest instrument available here and it is the right one. The
// snapshot is exactly what survives this process: it is what the purge claims is free of
// the tenant, and it is what a restart reads back. Asserting on the engine's internal maps
// would prove something about fields, and would keep passing if a structure were added to
// the engine and left out of the sweep; asserting on the serialized form fails the moment
// any tenant-bearing state reaches durability, whether or not this test knew the structure
// existed.
//
// The tenant token is chosen so that its appearance anywhere in the payload — rule id,
// series, map key, anything — is unambiguous.

// tenantRules returns one rule of every state-carrying kind, minted the way the runtime
// mints them: "{tenant}/{profileVersion}/{ruleToken}".
func tenantRules(tenant string) []Rule {
	out := statefulKinds(tenant + "/prof@1/")
	return out
}

// evictTenant applies the same predicate the processor's eviction builds: an exact prefix
// of "{tenant}/", never a substring and never a bare token compare.
func evictTenant(e *Engine, tenant string) int {
	return e.RemoveMatching(func(id string) bool { return strings.HasPrefix(id, tenant+"/") })
}

// TestEvictTenantLeavesNoTraceAndSparesTheOther is the load-bearing test of the slice.
//
// Two tenants hold live state across every keyed map, the timer wheel and the close heap.
// Evicting one must leave (a) no trace of it anywhere in the snapshot, and (b) the other's
// state so untouched that it detects exactly as it would in an engine that never held the
// evicted tenant at all — which is the property that separates an eviction from a reset,
// and the one a purge cannot be allowed to get wrong: a tenant deletion that quietly reset
// every other tenant's open windows would show up as missed alarms, not as an error.
func TestEvictTenantLeavesNoTraceAndSparesTheOther(t *testing.T) {
	victim, bystander := "acme", "globex"
	rulesV, rulesB := tenantRules(victim), tenantRules(bystander)
	e := NewEngine(append(append([]Rule{}, rulesV...), rulesB...), 0)
	var seq uint64
	feedSeq(e, &seq, accumulate(victim+"/prof@1/"))
	feedSeq(e, &seq, accumulate(bystander+"/prof@1/"))

	// Non-vacuity: the victim must actually be in the snapshot before the eviction, or
	// "it is not there afterwards" is a statement about an engine that never held it.
	before := snapString(t, e)
	if !strings.Contains(before, victim) {
		t.Fatalf("pre-eviction snapshot holds no state for %q, so the eviction below would "+
			"assert nothing: %s", victim, before)
	}

	if n := evictTenant(e, victim); n == 0 {
		t.Fatal("eviction reported removing nothing while the snapshot held the tenant's state")
	}

	// (a) NOT ONE OCCURRENCE OF THE TOKEN. Not "no rule ids" — the token also appears in
	// series names, and a sweep that reached the rule-keyed maps but missed, say, the
	// generation bookkeeping would leave the tenant's device tokens in the payload.
	after := snapString(t, e)
	if strings.Contains(after, victim) {
		t.Fatalf("post-eviction snapshot still names the evicted tenant %q: %s", victim, after)
	}
	if !strings.Contains(after, bystander) {
		t.Fatalf("post-eviction snapshot lost the surviving tenant's state: %s", after)
	}

	// (b) The bystander detects exactly as it would in an engine that never held the victim.
	gotBoth := feedSeq(e, &seq, fireAll(bystander+"/prof@1/"))

	clean := NewEngine(rulesB, 0)
	var cleanSeq uint64
	feedSeq(clean, &cleanSeq, accumulate(bystander+"/prof@1/"))
	gotClean := feedSeq(clean, &cleanSeq, fireAll(bystander+"/prof@1/"))

	if len(gotClean) == 0 {
		t.Fatal("test bug: the surviving tenant fired nothing, so the comparison is vacuous")
	}
	assertSetEqual(t, dedup(gotClean), dedup(gotBoth))
}

// TestEvictionClearsThePendingPaneCloses is the one assertion in this file that reaches
// inside the engine, and it is white-box because the black-box oracle is BLIND HERE.
//
// 🔴 Found by mutation: deleting the close-heap sweep from RemoveMatching left the entire
// suite green — including the pre-existing rule-removal test, which has the same hole. The
// reason is structural rather than an oversight in any one test: the close heap is not
// serialized (Restore rebuilds it from the surviving panes), so no assertion on snapshot
// bytes can ever see a leftover close item, no matter how thorough.
//
// What survives is not a wrong firing — closePanes tolerates a pane that is not there — it
// is the evicted tenant's rule id and device token sitting in this process's memory after
// a deletion record has reported them erased. That is the thing being erased, so it is
// worth the coupling to an internal field: the alternative is not a better test, it is no
// test.
func TestEvictionClearsThePendingPaneCloses(t *testing.T) {
	victim, bystander := "acme", "globex"
	e := NewEngine(append(tenantRules(victim), tenantRules(bystander)...), 0)
	var seq uint64
	feedSeq(e, &seq, accumulate(victim+"/prof@1/"))
	feedSeq(e, &seq, accumulate(bystander+"/prof@1/"))

	held := func(tenant string) int {
		n := 0
		for _, c := range e.closes {
			if strings.HasPrefix(c.pk.Rule, tenant+"/") {
				n++
			}
		}
		return n
	}
	if held(victim) == 0 {
		t.Fatal("test bug: no pending pane close for the victim, so the eviction below " +
			"would assert nothing")
	}
	survivorsBefore := held(bystander)

	evictTenant(e, victim)

	if n := held(victim); n != 0 {
		t.Fatalf("%d pending pane close(s) still name the evicted tenant, holding its rule ids "+
			"and device tokens in memory after the purge reported them erased", n)
	}
	if n := held(bystander); n != survivorsBefore {
		t.Fatalf("the eviction dropped %d of the surviving tenant's pending pane closes",
			survivorsBefore-n)
	}
}

// TestEvictedTenantStateDoesNotSurviveARestore closes the loop the whole slice exists for.
//
// The eviction's WHOLE PURPOSE is that the state is gone from what a restart reads back.
// An eviction that swept the live maps but left something the serializer still emitted —
// or that produced a payload Restore rebuilt state from — would satisfy every in-memory
// assertion and resurrect the tenant on the next pod restart, after the deletion record
// had already reported the erasure complete.
func TestEvictedTenantStateDoesNotSurviveARestore(t *testing.T) {
	victim, bystander := "acme", "globex"
	rulesV, rulesB := tenantRules(victim), tenantRules(bystander)
	e := NewEngine(append(append([]Rule{}, rulesV...), rulesB...), 0)
	var seq uint64
	feedSeq(e, &seq, accumulate(victim+"/prof@1/"))
	feedSeq(e, &seq, accumulate(bystander+"/prof@1/"))
	evictTenant(e, victim)
	snap := snapString(t, e)

	// Restored with only the surviving tenant's rules — which is what a restart does, since
	// the rule set is rebuilt from a durable projection the relational sweep has emptied of
	// the victim.
	restored, err := Restore(rulesB, 0, []byte(snap))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := snapString(t, restored)
	if strings.Contains(got, victim) {
		t.Fatalf("a restore rebuilt state for the evicted tenant %q: %s", victim, got)
	}
	if got != snap {
		t.Fatalf("the post-eviction snapshot is not stable across a restore:\n before=%s\n after =%s",
			snap, got)
	}
}

// TestEvictionIsAnchoredAtTheStartOfTheRuleId pins the matching rule against the two ways
// a prefix test goes wrong, both of which delete a live tenant's detection silently.
//
// 🔴 Neither failure announces itself. Evicting "acme" and taking "acme-2" with it, or
// taking "other/acme/…" with it, leaves the affected tenant with no rules and no state —
// so it simply stops alarming, with nothing in any log to connect that to a purge of a
// different tenant that happened to have a similar name.
func TestEvictionIsAnchoredAtTheStartOfTheRuleId(t *testing.T) {
	rules := []Rule{
		{ID: "acme/prof@1/hot", Kind: Duration, Hold: 100 * time.Second},
		// Shares the victim's token as a PREFIX but is a different tenant.
		{ID: "acme-2/prof@1/hot", Kind: Duration, Hold: 100 * time.Second},
		// CONTAINS the victim's token, in the profile position, but is owned by another.
		{ID: "other/acme@1/hot", Kind: Duration, Hold: 100 * time.Second},
	}
	e := NewEngine(rules, 0)
	var seq uint64
	for _, r := range rules {
		feedSeq(e, &seq, []step{evStep(0, r.ID, "dev", 1, true)})
	}

	before := snapString(t, e)
	for _, r := range rules {
		if !strings.Contains(before, r.ID) {
			t.Fatalf("test bug: %q holds no state to evict", r.ID)
		}
	}

	evictTenant(e, "acme")

	after := snapString(t, e)
	if strings.Contains(after, "acme/prof@1/hot") {
		t.Fatalf("the victim's own rule survived: %s", after)
	}
	for _, survivor := range []string{"acme-2/prof@1/hot", "other/acme@1/hot"} {
		if !strings.Contains(after, survivor) {
			t.Fatalf("evicting %q also took %q — a prefix test that is not anchored on the "+
				"tenant separator, or a substring test: %s", "acme", survivor, after)
		}
	}
}

// TestEvictionReachesStateThatOutlivedItsRule is the reason RemoveMatching sweeps the
// state structures rather than looking the tenant's rules up and removing those.
//
// The engine holds state for rule ids its rule map no longer contains — Restore reloads
// dead-man armings unconditionally, precisely so a rule that is republished later finds its
// once-per-epoch latch intact. A rules-driven eviction would walk an empty rule set,
// truthfully report zero, and leave the tenant's device tokens in the checkpoint. Rebuilt
// here the way a restart produces it: snapshot with the rule, restore WITHOUT it.
func TestEvictionReachesStateThatOutlivedItsRule(t *testing.T) {
	rule := Rule{ID: "acme/prof@1/quiet", Kind: Absence, Timeout: 100 * time.Second}
	e := NewEngine([]Rule{rule}, 0)
	e.SetExpected(SeriesKey{Rule: rule.ID, Series: "acme-sensor-1"}, at(0))
	snap := snapString(t, e)
	if !strings.Contains(snap, "acme-sensor-1") {
		t.Fatalf("test bug: no dead-man arming to strand: %s", snap)
	}

	orphaned, err := Restore(nil, 0, []byte(snap)) // no rules: the arming is now orphaned
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(orphaned.rules) != 0 {
		t.Fatal("test bug: the restored engine holds rules, so the state is not orphaned")
	}
	if got := snapString(t, orphaned); !strings.Contains(got, "acme-sensor-1") {
		t.Fatalf("test bug: the orphaned arming did not survive the restore: %s", got)
	}

	if n := evictTenant(orphaned, "acme"); n == 0 {
		t.Fatal("the eviction found nothing: state whose rule is gone is unreachable, which is " +
			"the tenant's device token left in the checkpoint after the purge reported clean")
	}
	if got := snapString(t, orphaned); strings.Contains(got, "acme") {
		t.Fatalf("orphaned state for the evicted tenant survived: %s", got)
	}
}

// TestEvictingAnAbsentTenantIsAZeroNoOp is the idempotency the coordinator's settle window
// depends on: a pass that finds nothing must report nothing, because any non-zero count
// restarts the window and a store that always reported a number would never settle.
func TestEvictingAnAbsentTenantIsAZeroNoOp(t *testing.T) {
	rules := tenantRules("globex")
	e := NewEngine(rules, 0)
	var seq uint64
	feedSeq(e, &seq, accumulate("globex/prof@1/"))
	before := snapString(t, e)

	if n := evictTenant(e, "acme"); n != 0 {
		t.Fatalf("evicting a tenant with no state reported %d entries removed", n)
	}
	if got := snapString(t, e); got != before {
		t.Fatalf("evicting an absent tenant changed the snapshot:\n before=%s\n after =%s",
			before, got)
	}

	// And a SECOND eviction of a tenant that was present reports zero, which is the same
	// property from the other side — this is the call the coordinator makes every pass.
	if n := evictTenant(e, "globex"); n == 0 {
		t.Fatal("test bug: the first eviction of the present tenant found nothing")
	}
	if n := evictTenant(e, "globex"); n != 0 {
		t.Fatalf("a repeated eviction reported %d entries, so the store would restart the "+
			"settle window on every pass and never complete", n)
	}
}

// TestEvictionDropsBufferedDetections covers the one piece of tenant state that is NOT in
// the snapshot: detections the engine has buffered but not yet drained.
//
// They are the tenant's data on their way OUT — each is published to that tenant's own
// subject — so a buffered detection surviving an eviction means an alarm emitted for a
// tenant whose erasure has already been reported, into a subject tree the broker purge has
// swept. Nothing in the snapshot assertions above can see this, because Snapshot
// deliberately does not serialize the buffer.
func TestEvictionDropsBufferedDetections(t *testing.T) {
	e := NewEngine([]Rule{
		{ID: "acme/prof@1/hot", Kind: Threshold, Op: GT, Thresh: 10},
		{ID: "globex/prof@1/hot", Kind: Threshold, Op: GT, Thresh: 10},
	}, 0)
	var seq uint64
	for _, id := range []string{"acme/prof@1/hot", "globex/prof@1/hot"} {
		seq++
		e.ProcessEvent(Event{
			Seq: seq, Key: SeriesKey{Rule: id, Series: "d"}, Time: at(int(seq)),
			Value: 50, HasValue: true, Match: true,
		})
	}
	if len(e.out) != 2 {
		t.Fatalf("test bug: expected two buffered detections, got %d", len(e.out))
	}

	evictTenant(e, "acme")

	got := e.Drain()
	if len(got) != 1 || got[0].RuleID != "globex/prof@1/hot" {
		t.Fatalf("eviction left a buffered detection for the evicted tenant (or dropped the "+
			"other tenant's): %+v", got)
	}
}
