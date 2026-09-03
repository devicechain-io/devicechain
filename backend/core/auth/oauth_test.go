// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"strings"
	"testing"
	"time"
)

// An OAuth access token round-trips through the ordinary access-token validator
// (so every existing JWKS consumer accepts it unchanged) and carries the granted
// scope plus the RFC 8707 audience binding.
func TestIssueOAuthAccess_RoundTrip(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueOAuthAccess("tenant-a", "alice@example.com",
		[]string{"viewer"}, []string{"device:read", "event:read"},
		ScopeReadOnly, []string{"https://mcp.example.com"}, false, "mcp-client", "jti-oauth")
	if err != nil {
		t.Fatalf("IssueOAuthAccess: %v", err)
	}
	claims, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Tenant != "tenant-a" {
		t.Errorf("tenant = %q, want tenant-a", claims.Tenant)
	}
	if claims.ClientId != "mcp-client" {
		t.Errorf("client_id = %q, want mcp-client", claims.ClientId)
	}
	if claims.Scope != ScopeReadOnly {
		t.Errorf("scope = %q, want %q", claims.Scope, ScopeReadOnly)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != "https://mcp.example.com" {
		t.Errorf("aud = %v, want [https://mcp.example.com]", claims.Audience)
	}
	if claims.Issuer != "https://as.example.com" {
		t.Errorf("iss = %q, want https://as.example.com", claims.Issuer)
	}
}

// A non-OAuth token carries neither scope nor audience — the fields are absent, so
// nothing about the existing token shapes changes.
func TestNonOAuthTokensHaveNoScopeOrAudience(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueAccess("tenant-a", "alice", []string{"admin"}, []string{string(AuthorityAll)}, "jti-plain")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	claims, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Scope != "" {
		t.Errorf("scope = %q, want empty", claims.Scope)
	}
	if len(claims.Audience) != 0 {
		t.Errorf("aud = %v, want empty", claims.Audience)
	}
	if claims.ClientId != "" {
		t.Errorf("client_id = %q, want empty on a non-OAuth token", claims.ClientId)
	}
}

// An OAuth refresh token carries the scope/audience so a refresh grant re-mints an
// access token with the same binding.
func TestIssueOAuthRefresh_CarriesScopeAndAudience(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueOAuthRefresh("tenant-a", "alice@example.com",
		[]string{"viewer"}, []string{"device:read"},
		ScopeReadOnly, []string{"https://mcp.example.com"}, "mcp-client", "jti-refresh")
	if err != nil {
		t.Fatalf("IssueOAuthRefresh: %v", err)
	}
	claims, err := v.ValidateRefresh(tok.Token)
	if err != nil {
		t.Fatalf("ValidateRefresh: %v", err)
	}
	if claims.Scope != ScopeReadOnly {
		t.Errorf("scope = %q, want %q", claims.Scope, ScopeReadOnly)
	}
	if len(claims.Audience) != 1 {
		t.Errorf("aud = %v, want one entry", claims.Audience)
	}
}

func TestScopeSupported(t *testing.T) {
	cases := []struct {
		scope string
		ok    bool
	}{
		{"", true}, // no scope requested is fine
		{ScopeReadOnly, true},
		{"read-only", true},
		{"read-only read-only", true},
		{ScopeLocation, true},
		{"read-only location", true},
		{"location read-only", true},
		{"write", false},
		{"read-only location write", false},
		{"read-only write", false},
		{"admin", false},
	}
	for _, tc := range cases {
		if got := ScopeSupported(tc.scope); got != tc.ok {
			t.Errorf("ScopeSupported(%q) = %v, want %v", tc.scope, got, tc.ok)
		}
	}
}

func TestIntersectAuthorities(t *testing.T) {
	allowed := []string{"device:read", "event:read", "state:read"}

	// A subject with a mix keeps only the allowed ones it holds, ordered by allowed.
	got := IntersectAuthorities([]string{"event:read", "device:write", "device:read"}, allowed)
	want := []string{"device:read", "event:read"}
	assertStrSlice(t, "mixed", got, want)

	// The super-authority is CAPPED to the allowance, never expanded, and "*" is
	// never itself returned — the load-bearing superuser-can't-exceed-scope guard.
	got = IntersectAuthorities([]string{string(AuthorityAll)}, allowed)
	assertStrSlice(t, "star capped", got, allowed)
	for _, a := range got {
		if a == string(AuthorityAll) {
			t.Fatalf("intersection leaked the super-authority")
		}
	}

	// A "*" smuggled into the allowance is dropped.
	got = IntersectAuthorities([]string{string(AuthorityAll)}, []string{"device:read", string(AuthorityAll)})
	assertStrSlice(t, "star in allowance dropped", got, []string{"device:read"})

	// No overlap → empty.
	if got := IntersectAuthorities([]string{"command:write"}, allowed); len(got) != 0 {
		t.Errorf("no overlap: got %v, want empty", got)
	}
	// Empty allowance → nil regardless of held.
	if got := IntersectAuthorities([]string{string(AuthorityAll)}, nil); got != nil {
		t.Errorf("empty allowance: got %v, want nil", got)
	}
	// Duplicate in allowance is de-duped.
	got = IntersectAuthorities([]string{"device:read"}, []string{"device:read", "device:read"})
	assertStrSlice(t, "dedup", got, []string{"device:read"})
}

func assertStrSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestValidateRedirectURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		ok   bool
	}{
		{"https ok", "https://client.example.com/callback", true},
		{"https with query ok", "https://client.example.com/cb?x=1", true},
		{"http loopback ok", "http://127.0.0.1:52100/callback", true},
		{"http localhost ok", "http://localhost:8080/cb", true},
		{"http ipv6 loopback ok", "http://[::1]:9000/cb", true},
		{"http non-loopback rejected", "http://client.example.com/cb", false},
		{"fragment rejected", "https://client.example.com/cb#frag", false},
		{"bare hash rejected", "https://client.example.com/cb#", false},
		{"custom scheme rejected", "com.example.app:/callback", false},
		{"relative rejected", "/callback", false},
		{"empty rejected", "", false},
		{"no host rejected", "https:///cb", false},
		{"userinfo host-spoof rejected", "https://good.com@evil.com/cb", false},
		{"userinfo on loopback rejected", "http://evil.com@127.0.0.1/cb", false},
		{"loopback-lookalike rejected", "http://127.0.0.1.evil.com/cb", false},
		{"trailing whitespace rejected", "https://client.example.com/cb ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRedirectURI(tc.uri)
			if tc.ok && err != nil {
				t.Errorf("ValidateRedirectURI(%q) = %v, want nil", tc.uri, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("ValidateRedirectURI(%q) = nil, want error", tc.uri)
			}
		})
	}
}

func TestIsSupportedScope(t *testing.T) {
	if !IsSupportedScope(ScopeReadOnly) {
		t.Errorf("read-only should be supported")
	}
	if IsSupportedScope("write") {
		t.Errorf("write should not be supported")
	}
	if IsSupportedScope("") {
		t.Errorf("empty should not be a supported scope")
	}
}

func TestParseScope(t *testing.T) {
	got := ParseScope("  read-only   foo ")
	if len(got) != 2 || got[0] != "read-only" || got[1] != "foo" {
		t.Errorf("ParseScope = %v, want [read-only foo]", got)
	}
	if len(ParseScope("")) != 0 {
		t.Errorf("ParseScope(\"\") should be empty")
	}
}

// 🔴 THE SCOPE CEILINGS, ASSERTED AS EXACT SETS AGAINST A LITERAL WRITTEN HERE.
//
// The literal is the point. An earlier version of this check compared
// ScopeAllowance's output against the very var it returns, which made it unable to
// fail for any value of that var — a reviewer widened the read-only ceiling with
// audit:read and connector:read and the whole suite stayed green, on a scope named
// "read-only" that would then have reached the tenant audit journal.
//
// So: any addition or removal must be made deliberately in two places. If this test
// fails, do not "sync" it — read what changed and decide whether a consent screen
// showing that scope name still describes what it now grants.
func TestScopeAllowancesAreExactlyThese(t *testing.T) {
	want := map[string][]string{
		"read-only": {"device:read", "event:read", "state:read", "command:read", "alarm:read", "dashboard:read"},
		"location":  {"location:read"},
	}

	// The scope VOCABULARY is pinned too, in both directions: a new supported scope
	// with no entry here would otherwise sail past every assertion below.
	if len(SupportedScopes) != len(want) {
		t.Fatalf("SupportedScopes = %v, but this test specifies %d scopes; a scope was "+
			"added or removed without deciding what its ceiling is", SupportedScopes, len(want))
	}
	for _, s := range SupportedScopes {
		if _, ok := want[s]; !ok {
			t.Fatalf("supported scope %q has no ceiling specified in this test", s)
		}
	}

	for scope, expected := range want {
		got, ok := ScopeAllowance(scope)
		if !ok {
			t.Errorf("ScopeAllowance(%q) reports the scope is undefined", scope)
			continue
		}
		if len(got) != len(expected) {
			t.Errorf("ScopeAllowance(%q) = %v, want exactly %v", scope, got, expected)
			continue
		}
		for _, a := range expected {
			if !containsString(got, a) {
				t.Errorf("ScopeAllowance(%q) = %v, missing %q", scope, got, a)
			}
		}
		for _, a := range got {
			if !containsString(expected, a) {
				t.Errorf("ScopeAllowance(%q) = %v, which contains the unspecified authority "+
					"%q — a ceiling grew without anyone deciding it should", scope, got, a)
			}
		}
	}
}

// Position is reachable ONLY through the `location` scope, never through `read-only`.
// This is the consent property: a resource owner must be able to authorize
// observability while withholding where a device — or a person — has been, and the
// only place that choice can be expressed is the scope they are shown.
func TestLocationIsReachableOnlyThroughItsOwnScope(t *testing.T) {
	ro, ok := ScopeAllowance(ScopeReadOnly)
	if !ok {
		t.Fatalf("read-only is not a defined scope")
	}
	if containsString(ro, string(LocationRead)) {
		t.Errorf("the read-only ceiling %v admits %q. A client asking only for read-only "+
			"would then receive position, and the consent screen — which renders the raw "+
			"scope strings — could not tell the resource owner that it had", ro, LocationRead)
	}
	loc, ok := ScopeAllowance(ScopeLocation)
	if !ok {
		t.Fatalf("location is not a defined scope")
	}
	if !containsString(loc, string(LocationRead)) {
		t.Errorf("the location ceiling %v does not admit %q, so nothing can ever grant "+
			"position through OAuth", loc, LocationRead)
	}
}

// Every ceiling is read-only and never names the super-authority. IntersectAuthorities
// already drops "*" from an allowance, so this is defence in depth on the table
// itself: a write authority here would let an OAuth session mutate the tenant, and
// the scopes are named for reading.
// 🔴 IT ITERATES THE TABLE, NOT SupportedScopes, AND THE DIFFERENCE IS THE WHOLE TEST.
// An earlier version walked SupportedScopes, so a ceiling whose key was absent from that
// list was invisible to every test in every package — an entry naming device:write,
// user:write and "*" could be added to scopeAllowances and all three packages stayed
// green. It is not reachable today, because ValidateAuthorizeRequest rejects an
// unadvertised scope at the front door and IntersectAuthorities drops "*" from an
// allowance. But scopeAllowance() never consults IsSupportedScope, so that gate lives at
// exactly one endpoint, and any future mint path that does not route through /authorize
// would make this table the authority. A table that is the authority has to be the thing
// the test reads.
func TestScopeAllowancesAreReadOnly(t *testing.T) {
	for scope := range scopeAllowances {
		allow, ok := ScopeAllowance(scope)
		if !ok {
			t.Errorf("scope %q has a ceiling in scopeAllowances but is not in SupportedScopes, so "+
				"ScopeAllowance refuses it — the two lists must agree", scope)
			continue
		}
		if len(allow) == 0 {
			t.Errorf("scope %q has an empty ceiling, so a token minted for it carries "+
				"nothing and the scope is unusable", scope)
		}
		for _, a := range allow {
			if a == string(AuthorityAll) {
				t.Errorf("scope %q names %q", scope, a)
				continue
			}
			if !strings.HasSuffix(a, ":read") {
				t.Errorf("scope %q names %q, which is not a read authority", scope, a)
			}
		}
	}
}

// An undefined scope fails closed, and the returned slice is a COPY — a caller that
// unioned into it in place would otherwise rewrite the ceiling for every later grant
// in the process. scopeAllowance in the AS does exactly that kind of union.
func TestScopeAllowanceFailsClosedAndCopies(t *testing.T) {
	if allow, ok := ScopeAllowance("write"); ok || allow != nil {
		t.Errorf(`ScopeAllowance("write") = %v, %v; an undefined scope must fail closed`, allow, ok)
	}
	if allow, ok := ScopeAllowance(""); ok || allow != nil {
		t.Errorf(`ScopeAllowance("") = %v, %v; the empty scope is not a scope`, allow, ok)
	}
	first, _ := ScopeAllowance(ScopeLocation)
	first[0] = "mutated"
	second, _ := ScopeAllowance(ScopeLocation)
	if second[0] == "mutated" {
		t.Errorf("ScopeAllowance handed out the package global: mutating one result changed "+
			"the next (%v)", second)
	}
}

// containsString is a local membership helper (the package targets no generics-heavy
// helpers and this keeps the assertions above readable).
func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
