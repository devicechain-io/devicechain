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
