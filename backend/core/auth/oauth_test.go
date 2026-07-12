// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
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
		ScopeReadOnly, []string{"https://mcp.example.com"}, false, "jti-oauth")
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
}

// An OAuth refresh token carries the scope/audience so a refresh grant re-mints an
// access token with the same binding.
func TestIssueOAuthRefresh_CarriesScopeAndAudience(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueOAuthRefresh("tenant-a", "alice@example.com",
		[]string{"viewer"}, []string{"device:read"},
		ScopeReadOnly, []string{"https://mcp.example.com"}, "jti-refresh")
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
		{"write", false},
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
