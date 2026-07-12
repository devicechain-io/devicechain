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

func TestParseScope(t *testing.T) {
	got := ParseScope("  read-only   foo ")
	if len(got) != 2 || got[0] != "read-only" || got[1] != "foo" {
		t.Errorf("ParseScope = %v, want [read-only foo]", got)
	}
	if len(ParseScope("")) != 0 {
		t.Errorf("ParseScope(\"\") should be empty")
	}
}
