// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "testing"

// A duplicate Compiled.ID is rejected at build: only one survives, so Lookup-based tenant
// attribution (the backstop) can never resolve a detection to the wrong rule, and byScope
// cannot feed the same rule twice.
func TestRegistryRejectsDuplicateID(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")},
		{Tenant: "beta", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")}, // duplicate id
	})
	if reg.Count() != 1 {
		t.Fatalf("duplicate id should leave one rule; got %d", reg.Count())
	}
	// The second (beta) filing was dropped, so beta selects nothing for that id.
	if got := reg.RulesFor("beta", "p@1"); len(got) != 0 {
		t.Fatalf("duplicate-id rule must not be filed under the second scope; got %d", len(got))
	}
}

// A rule with no owning tenant is dropped (it could never be attributed to a tenant subject).
func TestRegistryDropsEmptyTenant(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")}})
	if reg.Count() != 0 {
		t.Fatalf("empty-tenant rule should be dropped; got %d", reg.Count())
	}
}
