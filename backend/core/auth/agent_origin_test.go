// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestAgentOriginIsTotal pins every arm of the predicate, one at a time, so that dropping any
// arm turns exactly the row that depends on it red. "none" is the only false: an ordinary
// console token, which carries no client, scope, audience or proposal authority.
func TestAgentOriginIsTotal(t *testing.T) {
	cases := []struct {
		name   string
		claims *Claims
		want   bool
	}{
		{"nil claims are undecidable, so agent", nil, true},
		{"client_id only", &Claims{ClientId: "c"}, true},
		{"scope only", &Claims{Scope: "read-only"}, true},
		{"aud only", &Claims{RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"https://mcp.example.com"}}}, true},
		{"an empty-string aud member is still an audience", &Claims{RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{""}}}, true},
		{"proposal authority only", &Claims{Authorities: []string{"device:read", "command:propose"}}, true},
		{"proposal authority alone", &Claims{Authorities: []string{"alarm:propose"}}, true},
		{"none: a console token", &Claims{Tenant: "t", Username: "a@example.com", Authorities: []string{"device:read", "device:write"}, TokenType: TokenTypeAccess}, false},
		{"the super-authority is not a proposal", &Claims{Authorities: []string{string(AuthorityAll)}}, false},
		{"a write authority is not a proposal", &Claims{Authorities: []string{"command:write"}}, false},
		{"the suffix must END the name", &Claims{Authorities: []string{"command:propose:read"}}, false},
		{"zero claims, no token type, no tenant", &Claims{}, false},
		{"a service token with no client is not marked by this predicate", &Claims{TokenType: TokenTypeService, Username: "svc"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsAgentOrigin(c.claims); got != c.want {
				t.Fatalf("IsAgentOrigin = %v, want %v", got, c.want)
			}
		})
	}
}

// A token minted through every OAuth issuer path is agent-origin by the predicate: the
// predicate must not depend on a mint path remembering anything but the client id and scope.
func TestEveryOAuthMintIsAgentOrigin(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	oauth, err := iss.IssueOAuthAccess("t", "a@example.com", nil, []string{"device:read"},
		ScopeReadOnly, nil, false, "cli", "j1")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := iss.IssueScopedOAuthAccess("t", "a@example.com", nil, []string{"device:read"},
		ScopeReadOnly, nil, mustScope(t, "customer:acme"), "cli", "j2")
	if err != nil {
		t.Fatal(err)
	}
	sa, err := iss.IssueServiceAccountAccess("t", ServiceAccountSubject("bot"), nil,
		[]string{"device:read"}, "", nil, Scope{}, "bot-client", "j3")
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"oauth": oauth.Token, "scoped oauth": scoped.Token, "service account": sa.Token} {
		claims, err := v.Validate(tok)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !IsAgentOrigin(claims) {
			t.Errorf("%s token is not agent-origin: %+v", name, claims)
		}
	}
	// The console tier is the control: the same predicate says false for it.
	console, err := iss.IssueTenantAccess("t", "a@example.com", nil, []string{"device:read"}, false, "j4")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Validate(console.Token)
	if err != nil {
		t.Fatal(err)
	}
	if IsAgentOrigin(claims) {
		t.Errorf("a console token must not be agent-origin: %+v", claims)
	}
}

// An OAuth ACCESS token carries the sign-in's sid when the mint is given one, so a journal row
// or a gate can name the grant session. The claim survives the ordinary validator.
func TestOAuthAccessCarriesSid(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueOAuthAccess("t", "a@example.com", nil, []string{"device:read"},
		ScopeReadOnly, nil, false, "cli", "j1", WithSessionID("sid-1"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.SessionID != "sid-1" {
		t.Fatalf("sid = %q, want sid-1", claims.SessionID)
	}

	// The scoped variant too.
	stok, err := iss.IssueScopedOAuthAccess("t", "a@example.com", nil, []string{"device:read"},
		ScopeReadOnly, nil, mustScope(t, "customer:acme"), "cli", "j2", WithSessionID("sid-2"))
	if err != nil {
		t.Fatal(err)
	}
	sclaims, err := v.Validate(stok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if sclaims.SessionID != "sid-2" {
		t.Fatalf("scoped sid = %q, want sid-2", sclaims.SessionID)
	}

	// No option, no claim: an old-shape token is still valid and its sid is simply unknown.
	bare, err := iss.IssueOAuthAccess("t", "a@example.com", nil, nil, ScopeReadOnly, nil, false, "cli", "j3")
	if err != nil {
		t.Fatal(err)
	}
	bc, err := v.Validate(bare.Token)
	if err != nil {
		t.Fatalf("a token without sid must still validate: %v", err)
	}
	if bc.SessionID != "" {
		t.Fatalf("sid = %q, want empty", bc.SessionID)
	}

	// A service account never carries one.
	sa, err := iss.IssueServiceAccountAccess("t", ServiceAccountSubject("bot"), nil, nil, "", nil, Scope{}, "bot", "j4")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := v.Validate(sa.Token)
	if err != nil {
		t.Fatal(err)
	}
	if sc.SessionID != "" {
		t.Fatalf("a service-account token must carry no sid, got %q", sc.SessionID)
	}
}

// A missing sid is UNKNOWN and never a match, in either position and in both at once.
func TestMissingSidIsUnknownNotMatch(t *testing.T) {
	cases := []struct {
		name string
		a, b SessionID
		want bool
	}{
		{"equal", "s1", "s1", true},
		{"unequal", "s1", "s2", false},
		{"left missing", "", "s1", false},
		{"right missing", "s1", "", false},
		{"both missing", "", "", false},
	}
	for _, c := range cases {
		if got := SameSession(c.a, c.b); got != c.want {
			t.Errorf("%s: SameSession(%q, %q) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}
