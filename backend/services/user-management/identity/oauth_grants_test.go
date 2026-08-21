// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"slices"
	"strings"
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
	// A disabled client's tokens are rejected too — disable is a uniform kill switch,
	// including a public client refreshing with the token alone.
	disabledPublic := &iam.OAuthClient{Enabled: false}
	if e := checkRefreshClientBinding("mcp", "", disabledPublic, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("disabled public client: got %v, want invalid_grant", e)
	}
	disabledConfidential := &iam.OAuthClient{Enabled: false, SecretHash: "$2a$hash"}
	if e := checkRefreshClientBinding("grafana", "grafana", disabledConfidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("disabled confidential client: got %v, want invalid_grant", e)
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

// read-only maps to its own allowance (the viewer baseline plus location:read);
// unknown/empty scopes fail closed.
func TestScopeAllowance(t *testing.T) {
	allow, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only scope: %v", err)
	}
	if len(allow) != len(readOnlyScopeAllowance) {
		t.Errorf("read-only allowance = %v, want %v", allow, readOnlyScopeAllowance)
	}
	for _, want := range readOnlyScopeAllowance {
		if !slices.Contains(allow, want) {
			t.Errorf("read-only allowance %v is missing %q", allow, want)
		}
	}
	if _, err := scopeAllowance("write"); err == nil {
		t.Errorf("unknown scope should error")
	}
	if _, err := scopeAllowance(""); err == nil {
		t.Errorf("empty scope should error")
	}
}

// The `read-only` scope's allowance is a CEILING, not a baseline. It admits
// location:read — which the viewer baseline deliberately does not — and it must stay
// a superset of that baseline (a scope dropping one of the viewer's reads would
// silently shrink every OAuth session) while itself staying read-only.
func TestReadOnlyScopeAllowanceIsTheBaselinePlusLocation(t *testing.T) {
	if !slices.Contains(readOnlyScopeAllowance, string(auth.LocationRead)) {
		t.Errorf("the read-only scope allowance %v omits %q; MCP's query_locations is then "+
			"refusable-only, because the token endpoint strips the authority from every "+
			"caller including a superuser", readOnlyScopeAllowance, auth.LocationRead)
	}
	for _, a := range viewerAuthorities {
		if !slices.Contains(readOnlyScopeAllowance, a) {
			t.Errorf("the read-only scope allowance %v omits the viewer read %q, so a "+
				"read-only token would carry less than the console gives every member",
				readOnlyScopeAllowance, a)
		}
	}
	// Widening a ceiling is only safe while the ceiling stays read-only. This is the
	// property TestViewerAuthoritiesAreReadOnly asserts of the baseline, and it has to
	// be asserted here separately now that the two are no longer the same list.
	for _, a := range readOnlyScopeAllowance {
		if a == string(auth.AuthorityAll) {
			t.Errorf("the read-only scope allowance contains %q; a scope must never name "+
				"the super-authority", a)
			continue
		}
		if !strings.HasSuffix(a, ":read") {
			t.Errorf("the read-only scope allowance contains %q, which is not a read "+
				"authority — the scope names a read-only surface", a)
		}
	}
}

// A superuser's "*" is capped to the scope allowance rather than expanded, so while
// the allowance was the viewer baseline a tenant superuser minted a read-only token
// WITHOUT location:read and MCP's query_locations could not succeed for anybody. It
// does now.
func TestReadOnlyTokenForASuperuserCarriesLocationRead(t *testing.T) {
	allow, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only scope: %v", err)
	}
	capped := capToScope(nil, true, allow)
	if !slices.Contains(capped, string(auth.LocationRead)) {
		t.Errorf("a superuser's read-only token carries %v, without %q", capped, auth.LocationRead)
	}
	// The cap still holds in the direction that matters: "*" is never emitted.
	if slices.Contains(capped, string(auth.AuthorityAll)) {
		t.Errorf("a superuser's read-only token leaked %q: %v", auth.AuthorityAll, capped)
	}
}

// 🔴 The counterweight, and the more important half of the pair. Widening the CEILING
// must grant nobody anything: IntersectAuthorities emits only what the subject
// actually holds, so a member whose roles never granted location:read still mints a
// read-only token without it. If this test fails, the change widened the viewer
// BASELINE — which every enabled member receives whatever their roles — instead of
// the scope allowance, and the separation location:read exists to create is gone.
func TestReadOnlyTokenOmitsLocationReadWhenNoRoleGrantsIt(t *testing.T) {
	allow, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only scope: %v", err)
	}
	// A member with a role, just not a location-granting one.
	capped := capToScope([]string{string(auth.DeviceWrite)}, false, allow)
	if slices.Contains(capped, string(auth.LocationRead)) {
		t.Errorf("a member holding only %q minted a read-only token carrying %q: %v — the "+
			"scope allowance is a ceiling and must grant nothing on its own",
			auth.DeviceWrite, auth.LocationRead, capped)
	}
	// The precondition that keeps that absence from being vacuous: the cap really is
	// producing a populated token, so the missing authority is a restriction rather
	// than an empty result.
	if !slices.Contains(capped, string(auth.EventRead)) {
		t.Fatalf("precondition: a member's read-only token = %v, which does not even carry "+
			"%q, so its lack of location:read proves nothing", capped, auth.EventRead)
	}
	// A member with no roles at all — the viewer baseline and nothing else — likewise.
	if bare := capToScope(nil, false, allow); slices.Contains(bare, string(auth.LocationRead)) {
		t.Errorf("a member with no roles minted a read-only token carrying %q: %v — that "+
			"would mean the viewer baseline itself now grants it", auth.LocationRead, bare)
	}
	// And the case the widening exists for: a role that DOES grant it survives the cap.
	granted := capToScope([]string{string(auth.LocationRead)}, false, allow)
	if !slices.Contains(granted, string(auth.LocationRead)) {
		t.Errorf("a member whose role grants %q minted a read-only token without it: %v",
			auth.LocationRead, granted)
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
	// capping to the viewer baseline yields exactly the viewer baseline. (The
	// `read-only` scope's own allowance is a superset of this list — see
	// readOnlyScopeAllowance; what is asserted here is effectiveAuthorities, not the
	// scope.)
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
