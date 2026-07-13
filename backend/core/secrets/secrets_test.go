// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import "testing"

// TestSecretRefValid proves the fail-closed handle gate: a known scope with a
// name is valid, a tenant is required for tenant scope and forbidden for instance
// scope, an unknown scope is rejected, and a name is always required — so a
// malformed handle can never silently read or write across a scope boundary.
func TestSecretRefValid(t *testing.T) {
	cases := []struct {
		name string
		ref  SecretRef
		ok   bool
	}{
		{"instance ok", SecretRef{Scope: ScopeInstance, Name: "ai/provider/anthropic"}, true},
		{"tenant ok", SecretRef{Scope: ScopeTenant, Tenant: "acme", Name: "connector/1/auth"}, true},
		{"instance with tenant", SecretRef{Scope: ScopeInstance, Tenant: "acme", Name: "x"}, false},
		{"tenant without tenant", SecretRef{Scope: ScopeTenant, Name: "x"}, false},
		{"unknown scope", SecretRef{Scope: "global", Name: "x"}, false},
		{"missing name", SecretRef{Scope: ScopeInstance}, false},
		{"empty ref", SecretRef{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ref.Valid()
			if c.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want invalid, got nil")
			}
		})
	}
}
