// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// The fixture's admission decision, on its own: the C# tests that depend on it only see a refusal as
// "the connect failed", so a decision that admitted too much would pass their positive controls and
// fail nothing.
func TestAdmit(t *testing.T) {
	creds := map[string]string{"cred-a": "dev-a", "cred-b": "dev-b"}
	cases := []struct {
		name, username, clientID string
		want                     string
		ok                       bool
	}{
		{"own client id", "acme:cred-a", "inst:acme:dev-a", "dev-a", true},
		{"own client id with a discriminator", "acme:cred-a", "inst:acme:dev-a:second", "dev-a", true},
		{"another device's client id", "acme:cred-a", "inst:acme:dev-b", "", false},
		{"a client id that merely starts with the device token", "acme:cred-a", "inst:acme:dev-ab", "", false},
		{"unknown credential", "acme:cred-z", "inst:acme:dev-a", "", false},
		{"wrong tenant", "other:cred-a", "inst:acme:dev-a", "", false},
		{"no credential", "acme:", "inst:acme:dev-a", "", false},
		{"no separator", "cred-a", "inst:acme:dev-a", "", false},
		{"empty client id", "acme:cred-a", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := admit(c.username, c.clientID, "inst", "acme", creds)
			if ok != c.ok || got != c.want {
				t.Fatalf("admit(%q, %q) = (%q, %v), want (%q, %v)", c.username, c.clientID, got, ok, c.want, c.ok)
			}
		})
	}
}
