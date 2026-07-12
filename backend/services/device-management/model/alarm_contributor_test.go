// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"
)

func ts(sec int) time.Time { return time.Unix(int64(sec), 0).UTC() }

// edge is one contributor edge for the property tests.
type edge struct {
	rule   string
	raised bool
	tier   string
	ts     int
}

func applyAll(edges []edge) contributorSet {
	cs := contributorSet{}
	for _, e := range edges {
		cs.apply(e.rule, e.raised, e.tier, ts(e.ts))
	}
	return cs
}

// TestContributorRaiseThenResolveClears proves the core lifecycle: one rule raising makes the alarm
// active at its tier; the same rule resolving empties the active set (the alarm clears).
func TestContributorRaiseThenResolveClears(t *testing.T) {
	cs := contributorSet{}
	if !cs.apply("r1", true, "MAJOR", ts(10)) {
		t.Fatal("first raise must change the set")
	}
	sev, active := cs.activeSeverity()
	if !active || sev != "MAJOR" {
		t.Fatalf("one active contributor: want active MAJOR, got active=%v sev=%q", active, sev)
	}
	if !cs.apply("r1", false, "", ts(20)) {
		t.Fatal("resolve must change the set")
	}
	if _, active := cs.activeSeverity(); active {
		t.Fatal("after the only contributor resolves, no contributor is active (alarm clears)")
	}
}

// TestContributorMaxTierAcrossRules proves severity is the MAX tier over the active set, escalating
// when a higher-tier rule joins and de-escalating when the current max leaves — the emergent tiered
// "over-temp" behavior from independent per-rule edges sharing a key (ADR-057).
func TestContributorMaxTierAcrossRules(t *testing.T) {
	cs := contributorSet{}
	cs.apply("major", true, "MAJOR", ts(10))
	if sev, _ := cs.activeSeverity(); sev != "MAJOR" {
		t.Fatalf("want MAJOR, got %q", sev)
	}
	// A higher-tier rule joins → escalate to CRITICAL.
	cs.apply("crit", true, "CRITICAL", ts(11))
	if sev, _ := cs.activeSeverity(); sev != "CRITICAL" {
		t.Fatalf("a CRITICAL contributor must dominate; got %q", sev)
	}
	// The CRITICAL rule resolves → de-escalate back to MAJOR (still active via the other rule).
	cs.apply("crit", false, "", ts(12))
	if sev, active := cs.activeSeverity(); !active || sev != "MAJOR" {
		t.Fatalf("with CRITICAL gone, MAJOR remains: got active=%v sev=%q", active, sev)
	}
	// The MAJOR rule resolves → cleared.
	cs.apply("major", false, "", ts(13))
	if _, active := cs.activeSeverity(); active {
		t.Fatal("both rules resolved → cleared")
	}
}

// TestContributorResolveWinsAtEqualTs is the wire-contract tiebreak: a raise and a resolve for one
// rule sharing an event time net to RESOLVED regardless of application order (a zero-duration blip
// must not leave the alarm stuck raised).
func TestContributorResolveWinsAtEqualTs(t *testing.T) {
	// raise then resolve, both @10.
	a := contributorSet{}
	a.apply("r", true, "MAJOR", ts(10))
	a.apply("r", false, "", ts(10))
	if _, active := a.activeSeverity(); active {
		t.Fatal("raise@10 then resolve@10 must net to inactive (resolve wins)")
	}
	// resolve then raise, both @10 — the equal-ts tombstone blocks the re-add.
	b := contributorSet{}
	b.apply("r", false, "", ts(10))
	if b.apply("r", true, "MAJOR", ts(10)) {
		t.Fatal("a raise at the same ts as a resolve tombstone must be ignored (resolve wins)")
	}
	if _, active := b.activeSeverity(); active {
		t.Fatal("resolve@10 then raise@10 must remain inactive (resolve wins)")
	}
}

// TestContributorStaleEdgeIgnored proves an edge older than the stored decision time is ignored, so an
// out-of-order redelivery/replay can neither un-raise a newer raise nor re-raise a newer resolve.
func TestContributorStaleEdgeIgnored(t *testing.T) {
	cs := contributorSet{}
	cs.apply("r", true, "MAJOR", ts(10))
	if cs.apply("r", false, "", ts(5)) { // stale resolve (older than the raise)
		t.Fatal("a stale resolve must be ignored")
	}
	if _, active := cs.activeSeverity(); !active {
		t.Fatal("the alarm must stay raised after a stale resolve")
	}
	cs.apply("r", false, "", ts(20))          // genuine later resolve
	if cs.apply("r", true, "MAJOR", ts(15)) { // stale raise (older than the resolve)
		t.Fatal("a stale raise must be ignored")
	}
	if _, active := cs.activeSeverity(); active {
		t.Fatal("a stale raise must not reactivate a resolved contributor")
	}
}

// TestContributorIdempotentReRaise proves a duplicate raise (same tier, same ts) is a no-op, so an
// at-least-once redelivery does not report a spurious change.
func TestContributorIdempotentReRaise(t *testing.T) {
	cs := contributorSet{}
	if !cs.apply("r", true, "MAJOR", ts(10)) {
		t.Fatal("first raise changes")
	}
	if cs.apply("r", true, "MAJOR", ts(10)) {
		t.Fatal("an identical re-raise must be a no-op")
	}
}

// TestContributorOrderIndependence is the property test: for a fixed multiset of edges, EVERY
// application order yields the same active-severity and active-set — the guarantee that makes the
// at-least-once, out-of-order REACT stream re-derive one deterministic alarm state.
func TestContributorOrderIndependence(t *testing.T) {
	edges := []edge{
		{"a", true, "MAJOR", 10},
		{"a", false, "", 20},
		{"b", true, "CRITICAL", 15},
		{"b", true, "CRITICAL", 15}, // duplicate
		{"c", true, "MINOR", 12},
		{"c", false, "", 12}, // equal-ts resolve on c
		{"a", true, "WARNING", 25},
	}
	wantSev, wantActive := applyAll(edges).activeSeverity()
	// wantActiveKeys captures the exact active set for a stronger equality than severity alone.
	wantKeys := activeKeys(applyAll(edges))

	perms := 0
	permute(edges, 0, func(p []edge) {
		perms++
		got := applyAll(p)
		sev, active := got.activeSeverity()
		if sev != wantSev || active != wantActive {
			t.Fatalf("order dependence: perm %v gave active=%v sev=%q, want active=%v sev=%q", p, active, sev, wantActive, wantSev)
		}
		if !sameKeys(activeKeys(got), wantKeys) {
			t.Fatalf("order dependence in the active SET for perm %v", p)
		}
	})
	if perms == 0 {
		t.Fatal("no permutations exercised")
	}
}

func activeKeys(cs contributorSet) map[string]bool {
	out := map[string]bool{}
	for k, c := range cs {
		if c.Active {
			out[k] = true
		}
	}
	return out
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// permute invokes fn on every permutation of a copy of edges (Heap's algorithm).
func permute(edges []edge, k int, fn func([]edge)) {
	if k == len(edges) {
		cp := make([]edge, len(edges))
		copy(cp, edges)
		fn(cp)
		return
	}
	for i := k; i < len(edges); i++ {
		edges[k], edges[i] = edges[i], edges[k]
		permute(edges, k+1, fn)
		edges[k], edges[i] = edges[i], edges[k]
	}
}

// TestContributorUnknownTierStaysActive proves a forged/unknown tier keeps the alarm raised (counts as
// active) but does not participate in severity — a bogus tier cannot silently clear a live alarm.
func TestContributorUnknownTierStaysActive(t *testing.T) {
	cs := contributorSet{}
	cs.apply("good", true, "MAJOR", ts(10))
	cs.apply("bogus", true, "NONSENSE", ts(11))
	sev, active := cs.activeSeverity()
	if !active || sev != "MAJOR" {
		t.Fatalf("an unknown tier must not clear or dominate; want active MAJOR, got active=%v sev=%q", active, sev)
	}
}
