// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"net/url"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
)

// redirectURIMatches is the load-bearing OAuth control. Exact match everywhere,
// with the RFC 8252 §7.3 exception that a loopback registration matches any port.
func TestRedirectURIMatches(t *testing.T) {
	cases := []struct {
		name       string
		registered string
		requested  string
		want       bool
	}{
		{"exact https", "https://c.example.com/cb", "https://c.example.com/cb", true},
		{"https different path rejected", "https://c.example.com/cb", "https://c.example.com/evil", false},
		{"https extra subpath rejected", "https://c.example.com/cb", "https://c.example.com/cb/evil", false},
		{"https port must match", "https://c.example.com/cb", "https://c.example.com:8443/cb", false},
		{"loopback any port", "http://127.0.0.1/cb", "http://127.0.0.1:52100/cb", true},
		{"loopback registered-with-port matches other port", "http://127.0.0.1:5000/cb", "http://127.0.0.1:6000/cb", true},
		{"loopback localhost any port", "http://localhost/cb", "http://localhost:9000/cb", true},
		{"loopback ipv6 any port", "http://[::1]/cb", "http://[::1]:8080/cb", true},
		{"loopback different path rejected", "http://127.0.0.1/cb", "http://127.0.0.1:5000/evil", false},
		{"loopback different query rejected", "http://127.0.0.1/cb", "http://127.0.0.1:5000/cb?x=1", false},
		{"loopback scheme must match", "http://127.0.0.1/cb", "https://127.0.0.1/cb", false},
		{"loopback cannot match non-loopback", "http://127.0.0.1/cb", "http://evil.com/cb", false},
		{"non-loopback cannot be port-varied", "https://c.example.com/cb", "https://c.example.com./cb", false},
		{"registered loopback, requested loopback-lookalike rejected", "http://127.0.0.1/cb", "http://127.0.0.1.evil.com/cb", false},
		{"userinfo in requested loopback rejected", "http://127.0.0.1/cb", "http://evil.com@127.0.0.1:5000/cb", false},
		{"userinfo creds in requested loopback rejected", "http://127.0.0.1/cb", "http://u:p@127.0.0.1:5000/cb", false},
		{"fragment in requested loopback rejected", "http://127.0.0.1/cb", "http://127.0.0.1:5000/cb#frag", false},
		{"bare hash in requested loopback rejected", "http://127.0.0.1/cb", "http://127.0.0.1:5000/cb#", false},
		{"empty registered path matches requested root", "http://127.0.0.1", "http://127.0.0.1:5000/", true},
		{"empty registered path matches requested empty", "http://127.0.0.1", "http://127.0.0.1:5000", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectURIMatches(tc.registered, tc.requested); got != tc.want {
				t.Errorf("redirectURIMatches(%q, %q) = %v, want %v", tc.registered, tc.requested, got, tc.want)
			}
		})
	}
}

func TestRedirectURIRegistered(t *testing.T) {
	reg := []string{"https://a.example.com/cb", "http://127.0.0.1/cb"}
	if !redirectURIRegistered(reg, "http://127.0.0.1:5000/cb") {
		t.Errorf("loopback should match the registered loopback entry")
	}
	if redirectURIRegistered(reg, "https://a.example.com/evil") {
		t.Errorf("unregistered path must not match")
	}
	if redirectURIRegistered(nil, "https://a.example.com/cb") {
		t.Errorf("empty registry matches nothing")
	}
}

func TestParseAuthorizeParams(t *testing.T) {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {"mcp"},
		"redirect_uri":          {"http://127.0.0.1:5000/cb"},
		"scope":                 {"read-only"},
		"state":                 {"xyz"},
		"code_challenge":        {"abc"},
		"code_challenge_method": {"S256"},
		"resource":              {"https://mcp.example.com"},
	}
	p := ParseAuthorizeParams(v)
	if p.ResponseType != "code" || p.ClientID != "mcp" || p.RedirectURI != "http://127.0.0.1:5000/cb" ||
		p.Scope != "read-only" || p.State != "xyz" || p.CodeChallenge != "abc" ||
		p.CodeChallengeMethod != "S256" || p.Resource != "https://mcp.example.com" {
		t.Errorf("parsed params wrong: %+v", p)
	}
}

func TestValidateAuthorizeRequest(t *testing.T) {
	client := &iam.OAuthClient{Scopes: []string{"read-only"}}
	base := AuthorizeParams{ResponseType: "code", CodeChallenge: "abc", CodeChallengeMethod: "S256", Scope: "read-only"}

	if err := ValidateAuthorizeRequest(client, base); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	bad := func(mut func(*AuthorizeParams)) error {
		p := base
		mut(&p)
		return ValidateAuthorizeRequest(client, p)
	}
	assertRedirectErr := func(t *testing.T, err error, code string) {
		t.Helper()
		var re *authorizeRedirectError
		if err == nil {
			t.Fatalf("expected error %q, got nil", code)
		}
		if e, ok := err.(*authorizeRedirectError); ok {
			re = e
		} else {
			t.Fatalf("expected *authorizeRedirectError, got %T", err)
		}
		if re.Code != code {
			t.Errorf("error code = %q, want %q", re.Code, code)
		}
	}

	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.ResponseType = "token" }), "unsupported_response_type")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.CodeChallenge = "" }), "invalid_request")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.CodeChallengeMethod = "plain" }), "invalid_request")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.CodeChallengeMethod = "" }), "invalid_request")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.Scope = "" }), "invalid_scope")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.Scope = "   " }), "invalid_scope")
	assertRedirectErr(t, bad(func(p *AuthorizeParams) { p.Scope = "write" }), "invalid_scope")
}

// A scope the AS supports but the client is not registered for is rejected.
func TestValidateAuthorizeRequest_ScopeNotRegistered(t *testing.T) {
	client := &iam.OAuthClient{Scopes: []string{}} // registered for no scopes
	p := AuthorizeParams{ResponseType: "code", CodeChallenge: "abc", CodeChallengeMethod: "S256", Scope: "read-only"}
	err := ValidateAuthorizeRequest(client, p)
	if err == nil {
		t.Fatal("expected rejection for a scope the client is not registered for")
	}
	if e, ok := err.(*authorizeRedirectError); !ok || e.Code != "invalid_scope" {
		t.Errorf("want invalid_scope, got %v", err)
	}
}
