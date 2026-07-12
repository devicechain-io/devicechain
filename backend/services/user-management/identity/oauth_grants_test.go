// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// PKCE S256 verification against the RFC 7636 Appendix B test vector, plus the
// negative and empty cases (an empty verifier or challenge must never verify).
func TestVerifyPKCE(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	if !verifyPKCE(verifier, challenge) {
		t.Errorf("valid RFC 7636 verifier/challenge should verify")
	}
	if verifyPKCE("wrong-verifier", challenge) {
		t.Errorf("wrong verifier must not verify")
	}
	if verifyPKCE(verifier, "") || verifyPKCE("", challenge) || verifyPKCE("", "") {
		t.Errorf("empty verifier or challenge must never verify")
	}
	// A plain (non-hashed) verifier must not verify against an S256 challenge — we
	// only support S256, so a "plain" method attempt fails closed.
	if verifyPKCE(challenge, challenge) {
		t.Errorf("plain method (verifier==challenge) must not verify under S256")
	}
}

// read-only maps to the viewer baseline; unknown/empty scopes fail closed.
func TestScopeAllowance(t *testing.T) {
	allow, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only scope: %v", err)
	}
	if len(allow) != len(viewerAuthorities) {
		t.Errorf("read-only allowance = %v, want viewer baseline %v", allow, viewerAuthorities)
	}
	if _, err := scopeAllowance("write"); err == nil {
		t.Errorf("unknown scope should error")
	}
	if _, err := scopeAllowance(""); err == nil {
		t.Errorf("empty scope should error")
	}
}

func TestIsScopeSubset(t *testing.T) {
	if !isScopeSubset("read-only", "read-only") {
		t.Errorf("identical scope is a subset")
	}
	if !isScopeSubset("", "read-only") {
		t.Errorf("empty is a subset of anything")
	}
	if isScopeSubset("read-only write", "read-only") {
		t.Errorf("a superset must not be a subset")
	}
	if isScopeSubset("write", "read-only") {
		t.Errorf("disjoint is not a subset")
	}
}

// effectiveAuthorities gives the superuser "*" and unions the viewer baseline into
// a member's role authorities (mirroring issueTenantTokens) — the set the scope cap
// then intersects.
func TestEffectiveAuthorities(t *testing.T) {
	su := effectiveAuthorities(nil, true)
	if len(su) != 1 || su[0] != string(auth.AuthorityAll) {
		t.Errorf("superuser effective = %v, want [*]", su)
	}
	// A member holding only device:write still gets the viewer reads unioned in, so
	// capping to read-only yields the viewer baseline.
	member := effectiveAuthorities([]string{"device:write"}, false)
	capped := auth.IntersectAuthorities(member, viewerAuthorities)
	if len(capped) != len(viewerAuthorities) {
		t.Errorf("read-only cap of a member = %v, want viewer baseline", capped)
	}
	for _, a := range capped {
		if a == "device:write" || a == string(auth.AuthorityAll) {
			t.Errorf("read-only cap leaked a write/star authority: %v", capped)
		}
	}
}
