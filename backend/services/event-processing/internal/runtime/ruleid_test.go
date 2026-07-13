// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "testing"

func TestRuleIDTenantRoundTrip(t *testing.T) {
	id := ComposeRuleID("acme", "p@1/hot")
	tenant, ok := RuleTenant(id)
	if !ok || tenant != "acme" {
		t.Fatalf("RuleTenant(%q) = %q,%v; want acme,true", id, tenant, ok)
	}
}

func TestRuleTenantRejectsUnprefixed(t *testing.T) {
	for _, id := range []string{"rule1", "", "/rule1"} {
		if tenant, ok := RuleTenant(id); ok {
			t.Fatalf("RuleTenant(%q) = %q,true; want _,false", id, tenant)
		}
	}
}

// TestStableRuleKey proves the version-free identity: two ids differing only in the profile VERSION
// yield the SAME stable key (so a re-publish does not fork a rule's alarm), and a malformed id fails.
func TestStableRuleKey(t *testing.T) {
	k1, ok1 := StableRuleKey(ComposeRuleID("acme", "prof@1/over-temp"))
	k2, ok2 := StableRuleKey(ComposeRuleID("acme", "prof@2/over-temp"))
	if !ok1 || !ok2 {
		t.Fatalf("well-formed ids must parse: %v %v", ok1, ok2)
	}
	if k1 != "prof/over-temp" || k1 != k2 {
		t.Fatalf("stable key must be version-free and equal across versions: %q vs %q", k1, k2)
	}
	// A different profile or rule token yields a different key (no cross-rule collision).
	other, _ := StableRuleKey(ComposeRuleID("acme", "prof@1/over-pressure"))
	if other == k1 {
		t.Fatalf("distinct rule tokens must not collide")
	}
	for _, bad := range []string{"", "acme", "acme/norule", "acme/prof/rule", "acme/@1/rule", "acme/prof@1/"} {
		if key, ok := StableRuleKey(bad); ok {
			t.Fatalf("StableRuleKey(%q) = %q,true; want _,false", bad, key)
		}
	}
}

// TestProfileAndRuleToken proves the detection-stream filter's id parse: it recovers the profile
// and rule tokens from a minted id regardless of version, and rejects malformed ids (which the
// filter treats as non-matching, so a garbled event is skipped rather than mis-attributed).
func TestProfileAndRuleToken(t *testing.T) {
	pt, rt, ok := ProfileAndRuleToken(PublishedRuleID("acme", "thermostat@3", "over-temp"))
	if !ok || pt != "thermostat" || rt != "over-temp" {
		t.Fatalf("ProfileAndRuleToken = %q,%q,%v; want thermostat,over-temp,true", pt, rt, ok)
	}
	// The version is stripped: a different version of the same profile parses to the same profile token.
	pt2, _, _ := ProfileAndRuleToken(PublishedRuleID("acme", "thermostat@9", "over-temp"))
	if pt2 != pt {
		t.Fatalf("version leaked into profile token: %q vs %q", pt2, pt)
	}
	for _, bad := range []string{"", "acme", "acme/norule", "acme/prof/rule", "acme/@1/rule", "acme/prof@1/"} {
		if p, r, ok := ProfileAndRuleToken(bad); ok {
			t.Fatalf("ProfileAndRuleToken(%q) = %q,%q,true; want _,_,false", bad, p, r)
		}
	}
}
