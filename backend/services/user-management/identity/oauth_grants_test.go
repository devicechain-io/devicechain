// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"slices"
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
	confidential := &iam.OAuthClient{Enabled: true, SecretHash: "$2a$hash", Scopes: []string{auth.ScopeReadOnly}}
	public := &iam.OAuthClient{Enabled: true, Scopes: []string{auth.ScopeReadOnly}}

	// Unbound token (no client_id claim): always allowed, no lookup consulted.
	if e := checkRefreshClientBinding("", "", auth.ScopeReadOnly, nil, false); e != nil {
		t.Errorf("unbound: got %v, want nil", e)
	}
	// Confidential bound token: allowed only when the authenticated client matches.
	if e := checkRefreshClientBinding("grafana", "grafana", auth.ScopeReadOnly, confidential, true); e != nil {
		t.Errorf("confidential + matching client: got %v, want nil", e)
	}
	// THE EXPLOIT: a stolen refresh token presented with no client credentials.
	if e := checkRefreshClientBinding("grafana", "", auth.ScopeReadOnly, confidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("confidential + no authenticated client: got %v, want invalid_grant", e)
	}
	// Cross-client: another confidential client cannot refresh this token.
	if e := checkRefreshClientBinding("grafana", "other", auth.ScopeReadOnly, confidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("confidential + wrong client: got %v, want invalid_grant", e)
	}
	// Public bound token: lenient — refreshable with the token alone (no secret exists).
	if e := checkRefreshClientBinding("mcp", "", auth.ScopeReadOnly, public, true); e != nil {
		t.Errorf("public bound + no client: got %v, want nil (lenient)", e)
	}
	// A deleted client's tokens are rejected (deletion kills sessions).
	if e := checkRefreshClientBinding("gone", "gone", auth.ScopeReadOnly, nil, false); e == nil || e.Code != "invalid_grant" {
		t.Errorf("deleted client: got %v, want invalid_grant", e)
	}
	// A disabled client's tokens are rejected too — disable is a uniform kill switch,
	// including a public client refreshing with the token alone.
	disabledPublic := &iam.OAuthClient{Enabled: false, Scopes: []string{auth.ScopeReadOnly}}
	if e := checkRefreshClientBinding("mcp", "", auth.ScopeReadOnly, disabledPublic, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("disabled public client: got %v, want invalid_grant", e)
	}
	disabledConfidential := &iam.OAuthClient{Enabled: false, SecretHash: "$2a$hash", Scopes: []string{auth.ScopeReadOnly}}
	if e := checkRefreshClientBinding("grafana", "grafana", auth.ScopeReadOnly, disabledConfidential, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("disabled confidential client: got %v, want invalid_grant", e)
	}
	// A scope the client is no longer registered for is refused — de-registering a
	// scope has to end the sessions holding it, or the only working kill switches are
	// disabling the client and deleting it.
	narrowed := &iam.OAuthClient{Enabled: true, Scopes: []string{auth.ScopeReadOnly}}
	if e := checkRefreshClientBinding("mcp", "", auth.ScopeReadOnly+" "+auth.ScopeLocation, narrowed, true); e == nil || e.Code != "invalid_grant" {
		t.Errorf("de-registered scope: got %v, want invalid_grant", e)
	}
	// The counterweight: a still-registered multi-scope grant refreshes normally.
	both := &iam.OAuthClient{Enabled: true, Scopes: []string{auth.ScopeReadOnly, auth.ScopeLocation}}
	if e := checkRefreshClientBinding("mcp", "", auth.ScopeReadOnly+" "+auth.ScopeLocation, both, true); e != nil {
		t.Errorf("registered multi-scope: got %v, want nil", e)
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

// 🔴 THE CEILING FOR EACH SCOPE, AS AN EXACT SET AGAINST A LITERAL WRITTEN HERE.
//
// The literal matters more than it looks. A previous version compared
// scopeAllowance()'s output against the very var scopeAllowance() returns, which
// made it incapable of failing for any value of that var: a reviewer added
// audit:read and connector:read to the read-only ceiling and the whole package
// stayed green — both are tenant-tier authorities, so both are grantable on a tenant
// role and satisfiable on the very token this caps, meaning a scope literally named
// "read-only" would have reached the tenant audit journal with no test firing.
//
// core/auth pins the same sets from the other side (TestScopeAllowancesAreExactlyThese).
// Two lists, written independently, both of which must be edited on purpose.
func TestScopeAllowance(t *testing.T) {
	cases := []struct {
		scope string
		want  []string
	}{
		{auth.ScopeReadOnly, []string{"device:read", "event:read", "state:read", "command:read", "alarm:read"}},
		{auth.ScopeLocation, []string{"location:read"}},
		// The multi-scope request an MCP client actually makes: the union of both
		// ceilings, deduplicated, with nothing extra.
		{"read-only location", []string{"device:read", "event:read", "state:read", "command:read", "alarm:read", "location:read"}},
		// A repeated scope is not a wider scope.
		{"location location", []string{"location:read"}},
	}
	for _, tc := range cases {
		got, err := scopeAllowance(tc.scope)
		if err != nil {
			t.Errorf("scopeAllowance(%q): %v", tc.scope, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("scopeAllowance(%q) = %v, want exactly %v", tc.scope, got, tc.want)
			continue
		}
		for _, a := range tc.want {
			if !slices.Contains(got, a) {
				t.Errorf("scopeAllowance(%q) = %v, missing %q", tc.scope, got, a)
			}
		}
		for _, a := range got {
			if !slices.Contains(tc.want, a) {
				t.Errorf("scopeAllowance(%q) = %v, which admits the unspecified authority %q "+
					"— a ceiling grew without anyone deciding it should", tc.scope, got, a)
			}
		}
	}

	if _, err := scopeAllowance("write"); err == nil {
		t.Errorf("unknown scope should error")
	}
	if _, err := scopeAllowance(""); err == nil {
		t.Errorf("empty scope should error")
	}
	// 🔴 Fail-closed on the MIXED case too: one unknown member poisons the whole
	// request rather than being quietly dropped, which would mint a token for a
	// narrower scope than the client believes it holds.
	if _, err := scopeAllowance("read-only write"); err == nil {
		t.Errorf("a scope set containing an unknown member should error")
	}
}

// 🔴 THE TWO LISTS THAT MUST STAY IN STEP, AND THE REASON THEY ARE TWO.
//
// `read-only` names the same SURFACE the console's viewer baseline covers, but the
// allowance is a CEILING (the most a token may carry) and viewerAuthorities is a
// GRANT (what every enabled member receives regardless of role). Wiring one to the
// other is what produced the defect: the read-only ceiling WAS viewerAuthorities, so
// the baseline's deliberate omission of location:read became a refusal for every
// OAuth caller including a tenant superuser, and MCP's query_locations could not be
// called by anybody.
//
// Keeping them equal is still right — the scope should reach exactly the viewer
// surface — but it has to be a decision each time, which is why this is an assertion
// and not an assignment. If this fails because the baseline gained a read, decide
// whether a client authorized for "read-only" should now receive it too, then edit
// core/auth's table on purpose.
func TestReadOnlyCeilingMatchesTheViewerBaselineExactly(t *testing.T) {
	allow, ok := auth.ScopeAllowance(auth.ScopeReadOnly)
	if !ok {
		t.Fatal("read-only is not a defined scope")
	}
	if len(allow) != len(viewerAuthorities) {
		t.Fatalf("the read-only ceiling %v and the viewer baseline %v have diverged in size",
			allow, viewerAuthorities)
	}
	for _, a := range viewerAuthorities {
		if !slices.Contains(allow, a) {
			t.Errorf("the viewer baseline grants %q but the read-only ceiling %v does not "+
				"admit it, so an OAuth session carries less than the console does", a, allow)
		}
	}
	for _, a := range allow {
		if !slices.Contains(viewerAuthorities, a) {
			t.Errorf("the read-only ceiling admits %q, which the viewer baseline does not "+
				"grant: the scope now reaches beyond the surface its name describes", a)
		}
	}
	// And the separation the whole item exists for, asserted from this side too.
	if slices.Contains(allow, string(auth.LocationRead)) {
		t.Errorf("the read-only ceiling admits %q; position must require its own scope so "+
			"the consent screen can name it", auth.LocationRead)
	}
}

// A superuser's "*" is capped to the allowance rather than expanded. With the
// `location` scope requested it now carries position; with `read-only` alone it does
// not, however total the subject's authorities are. That second half is the consent
// property: a client the resource owner never approved for location cannot reach it.
func TestSuperuserNeedsTheLocationScopeForPosition(t *testing.T) {
	both, err := scopeAllowance("read-only location")
	if err != nil {
		t.Fatalf("read-only location: %v", err)
	}
	capped := capToScope(nil, true, both)
	if !slices.Contains(capped, string(auth.LocationRead)) {
		t.Errorf("a superuser granted read-only+location carries %v, without %q", capped, auth.LocationRead)
	}
	if slices.Contains(capped, string(auth.AuthorityAll)) {
		t.Errorf("a superuser's scoped token leaked %q: %v", auth.AuthorityAll, capped)
	}

	ro, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only: %v", err)
	}
	roCapped := capToScope(nil, true, ro)
	if slices.Contains(roCapped, string(auth.LocationRead)) {
		t.Errorf("a superuser granted only read-only carries %q: %v — the scope the resource "+
			"owner approved must bound the session even for a superuser",
			auth.LocationRead, roCapped)
	}
	// Precondition: read-only is doing real work, so the absence above is a cap
	// rather than an empty result.
	if !slices.Contains(roCapped, string(auth.EventRead)) {
		t.Fatalf("precondition: a superuser's read-only token = %v, which carries no %q, so "+
			"its lack of location:read proves nothing", roCapped, auth.EventRead)
	}
}

// 🔴 THE COUNTERWEIGHT, and the more important half. A scope is a CEILING:
// IntersectAuthorities emits only what the subject actually holds, so REQUESTING
// `location` grants nothing. A member whose roles never gave them location:read mints
// a token without it even when the client asked for the scope and the resource owner
// approved it.
//
// If this fails, the change widened the viewer BASELINE — which every enabled member
// receives whatever their roles — instead of defining a scope, and the separation
// location:read exists to create is gone.
func TestLocationScopeGrantsNothingOnItsOwn(t *testing.T) {
	allow, err := scopeAllowance("read-only location")
	if err != nil {
		t.Fatalf("read-only location: %v", err)
	}

	// A member with a role, just not a location-granting one.
	capped := capToScope([]string{string(auth.DeviceWrite)}, false, allow)
	if slices.Contains(capped, string(auth.LocationRead)) {
		t.Errorf("a member holding only %q minted a token carrying %q: %v — asking for the "+
			"scope must not grant the authority", auth.DeviceWrite, auth.LocationRead, capped)
	}
	// The precondition that keeps that absence from being vacuous.
	if !slices.Contains(capped, string(auth.EventRead)) {
		t.Fatalf("precondition: the member's token = %v, which carries no %q, so its lack of "+
			"location:read proves nothing", capped, auth.EventRead)
	}
	// A member with no roles at all — the viewer baseline and nothing else.
	if bare := capToScope(nil, false, allow); slices.Contains(bare, string(auth.LocationRead)) {
		t.Errorf("a member with no roles minted a token carrying %q: %v — the viewer baseline "+
			"itself would then grant position", auth.LocationRead, bare)
	}
	// And the case the scope exists for: a role that DOES grant it survives the cap.
	granted := capToScope([]string{string(auth.LocationRead)}, false, allow)
	if !slices.Contains(granted, string(auth.LocationRead)) {
		t.Errorf("a member whose role grants %q minted a token without it: %v",
			auth.LocationRead, granted)
	}
	// ...but only when the scope was granted. Role AND scope, both required.
	ro, err := scopeAllowance(auth.ScopeReadOnly)
	if err != nil {
		t.Fatalf("read-only: %v", err)
	}
	if roGranted := capToScope([]string{string(auth.LocationRead)}, false, ro); slices.Contains(roGranted, string(auth.LocationRead)) {
		t.Errorf("a member whose role grants %q received it on a read-only-scoped token: %v",
			auth.LocationRead, roGranted)
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
	// capping to the viewer baseline yields exactly the viewer baseline. What is
	// asserted here is effectiveAuthorities, not a scope's ceiling — the `read-only`
	// ceiling is exactly EQUAL to this list, which
	// TestReadOnlyCeilingMatchesTheViewerBaselineExactly pins separately.
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
