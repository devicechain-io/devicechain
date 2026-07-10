// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "testing"

func scopedThreshold(tenant, pv, id string) ScopedRule {
	return ScopedRule{Tenant: tenant, ProfileVersionToken: pv, Compiled: thresholdRule(id)}
}

// Upsert files a brand-new rule under its scope and by id.
func TestUpsertAddsNewRule(t *testing.T) {
	reg := NewRuleRegistry(nil)
	if !reg.Upsert(scopedThreshold("acme", "p@1", "acme/p@1/r1")) {
		t.Fatal("upsert of a valid rule should be admitted")
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1", reg.Count())
	}
	if got := reg.RulesFor("acme", "p@1"); len(got) != 1 {
		t.Fatalf("RulesFor = %d, want 1", len(got))
	}
	if _, ok := reg.Lookup("acme/p@1/r1"); !ok {
		t.Fatal("Lookup should find the upserted rule")
	}
	if len(reg.Cores()) != 1 {
		t.Fatalf("Cores = %d, want 1", len(reg.Cores()))
	}
}

// Re-upserting the same id (a redelivered published-rule fact) replaces in place — it does not
// double the scope bucket or the count.
func TestUpsertReplaceIsIdempotent(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{scopedThreshold("acme", "p@1", "acme/p@1/r1")})
	if !reg.Upsert(scopedThreshold("acme", "p@1", "acme/p@1/r1")) {
		t.Fatal("re-upsert should be admitted")
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1 (no duplicate)", reg.Count())
	}
	if got := reg.RulesFor("acme", "p@1"); len(got) != 1 {
		t.Fatalf("RulesFor = %d, want 1 (scope bucket not doubled)", len(got))
	}
}

// Upsert refuses a nil compiled rule and an empty owning tenant (fail-closed), touching nothing.
func TestUpsertRejectsInvalid(t *testing.T) {
	reg := NewRuleRegistry(nil)
	if reg.Upsert(ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: nil}) {
		t.Fatal("nil compiled rule must be refused")
	}
	if reg.Upsert(ScopedRule{Tenant: "", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/p@1/r1")}) {
		t.Fatal("empty-tenant rule must be refused")
	}
	if reg.Count() != 0 {
		t.Fatalf("count = %d, want 0", reg.Count())
	}
}

// Remove drops the rule from byID and its scope bucket; a second remove reports absence.
func TestRemoveDropsRuleAndScope(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{scopedThreshold("acme", "p@1", "acme/p@1/r1")})
	if !reg.Remove("acme/p@1/r1") {
		t.Fatal("remove of a present rule should report true")
	}
	if reg.Count() != 0 {
		t.Fatalf("count = %d, want 0", reg.Count())
	}
	if got := reg.RulesFor("acme", "p@1"); len(got) != 0 {
		t.Fatalf("RulesFor = %d, want 0 (scope bucket emptied)", len(got))
	}
	if _, ok := reg.Lookup("acme/p@1/r1"); ok {
		t.Fatal("Lookup should not find a removed rule")
	}
	if reg.Remove("acme/p@1/r1") {
		t.Fatal("removing an absent rule should report false")
	}
}

// Removing one rule leaves its scope peers intact.
func TestRemoveSparesScopePeers(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		scopedThreshold("acme", "p@1", "acme/p@1/r1"),
		scopedThreshold("acme", "p@1", "acme/p@1/r2"),
	})
	reg.Remove("acme/p@1/r1")
	got := reg.RulesFor("acme", "p@1")
	if len(got) != 1 || got[0].Compiled.ID != "acme/p@1/r2" {
		t.Fatalf("scope peer not preserved after removal: %+v", got)
	}
	if len(reg.Cores()) != 1 {
		t.Fatalf("Cores = %d, want 1 after removal", len(reg.Cores()))
	}
}

// A published-rule fact for a NEW version adds a new scope while the superseded version's rules
// are retained (the intended retain-superseded behavior; both scopes select independently).
func TestUpsertNewVersionRetainsOld(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{scopedThreshold("acme", "p@1", "acme/p@1/r1")})
	reg.Upsert(scopedThreshold("acme", "p@2", "acme/p@2/r1"))
	if got := reg.RulesFor("acme", "p@1"); len(got) != 1 {
		t.Fatalf("superseded version's rules should be retained; RulesFor(p@1) = %d", len(got))
	}
	if got := reg.RulesFor("acme", "p@2"); len(got) != 1 {
		t.Fatalf("new version's rules should be selectable; RulesFor(p@2) = %d", len(got))
	}
	if reg.Count() != 2 {
		t.Fatalf("count = %d, want 2 (both versions live)", reg.Count())
	}
}
