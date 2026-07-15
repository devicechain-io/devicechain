// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"golang.org/x/crypto/bcrypt"
)

// verifyClientAuth is the pure token-endpoint client-authentication decision: a
// disabled client is always rejected; a confidential client must present a
// bcrypt-matching secret; a public client must NOT present a secret.
func TestVerifyClientAuth(t *testing.T) {
	const secret = "s3cr3t-value"
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	confidential := &iam.OAuthClient{Enabled: true, SecretHash: string(hash)}
	public := &iam.OAuthClient{Enabled: true}

	// Confidential: correct secret passes; wrong/absent secret is invalid_client.
	if e := verifyClientAuth(confidential, secret, true); e != nil {
		t.Errorf("confidential + correct secret: got %v, want nil", e)
	}
	if e := verifyClientAuth(confidential, "wrong", true); e == nil || e.Code != "invalid_client" {
		t.Errorf("confidential + wrong secret: got %v, want invalid_client", e)
	}
	if e := verifyClientAuth(confidential, "", false); e == nil || e.Code != "invalid_client" {
		t.Errorf("confidential + no secret: got %v, want invalid_client", e)
	}

	// Public: no secret passes; a presented secret is a misconfiguration → invalid_client.
	if e := verifyClientAuth(public, "", false); e != nil {
		t.Errorf("public + no secret: got %v, want nil", e)
	}
	if e := verifyClientAuth(public, "anything", true); e == nil || e.Code != "invalid_client" {
		t.Errorf("public + presented secret: got %v, want invalid_client", e)
	}

	// A disabled client is rejected regardless of type/secret.
	for _, c := range []*iam.OAuthClient{
		{Enabled: false, SecretHash: string(hash)},
		{Enabled: false},
	} {
		if e := verifyClientAuth(c, secret, true); e == nil || e.Code != "invalid_client" {
			t.Errorf("disabled client: got %v, want invalid_client", e)
		}
	}
}

// checkRefreshClientBinding is the refresh-grant client-binding rule: a confidential
// client's refresh token requires the request to be that same authenticated client;
// public/unbound tokens stay lenient; a deleted client's tokens are rejected.
func TestCheckRefreshClientBinding(t *testing.T) {
	confidential := &iam.OAuthClient{Enabled: true, SecretHash: "$2a$hash"}
	public := &iam.OAuthClient{Enabled: true}

	// Unbound token (no client_id claim): always allowed, no lookup consulted.
	if e := checkRefreshClientBinding("", "", nil, false); e != nil {
		t.Errorf("unbound: got %v, want nil", e)
	}
	// Confidential bound token: allowed only when the authenticated client matches.
	if e := checkRefreshClientBinding("grafana", "grafana", confidential, true); e != nil {
		t.Errorf("confidential + matching client: got %v, want nil", e)
	}
	// THE EXPLOIT: a stolen refresh token presented with no client credentials.
	if e := checkRefreshClientBinding("grafana", "", confidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("confidential + no authenticated client: got %v, want invalid_grant", e)
	}
	// Cross-client: another confidential client cannot refresh this token.
	if e := checkRefreshClientBinding("grafana", "other", confidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("confidential + wrong client: got %v, want invalid_grant", e)
	}
	// Public bound token: lenient — refreshable with the token alone (no secret exists).
	if e := checkRefreshClientBinding("mcp", "", public, true); e != nil {
		t.Errorf("public bound + no client: got %v, want nil (lenient)", e)
	}
	// A deleted client's tokens are rejected (deletion kills sessions).
	if e := checkRefreshClientBinding("gone", "gone", nil, false); e == nil || e.Code != "invalid_grant" {
		t.Errorf("deleted client: got %v, want invalid_grant", e)
	}
}

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
