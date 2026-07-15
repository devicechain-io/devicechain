// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The metadata document advertises the issuer verbatim and derives every endpoint
// URL from it, so a client that discovered the AS at <issuer> finds absolute URLs
// under that same origin (ADR-047 / RFC 8414).
func TestBuildAuthorizationServerMetadata(t *testing.T) {
	const issuer = "https://devicechain.example.com/user-management"
	md := BuildAuthorizationServerMetadata(issuer)

	if md.Issuer != issuer {
		t.Errorf("issuer = %q, want %q", md.Issuer, issuer)
	}
	if md.AuthorizationEndpoint != issuer+"/oauth/authorize" {
		t.Errorf("authorization_endpoint = %q", md.AuthorizationEndpoint)
	}
	if md.TokenEndpoint != issuer+"/oauth/token" {
		t.Errorf("token_endpoint = %q", md.TokenEndpoint)
	}
	// jwks_uri is the public /oauth/jwks mirror, not the ingress-blocked /auth/jwks.
	if md.JwksURI != issuer+"/oauth/jwks" {
		t.Errorf("jwks_uri = %q", md.JwksURI)
	}
	// The advertised surface is the narrow, secure one: code + refresh grants,
	// PKCE S256 only, public clients (no secret), and only scopes we grant.
	assertContains(t, "response_types", md.ResponseTypesSupported, "code")
	assertContains(t, "grant_types", md.GrantTypesSupported, "authorization_code")
	assertContains(t, "grant_types", md.GrantTypesSupported, "refresh_token")
	assertContains(t, "code_challenge_methods", md.CodeChallengeMethodsSupported, "S256")
	// Public (none) AND confidential (client_secret_basic/post) authentication are
	// advertised — the confidential-client fold-in.
	assertContains(t, "token_endpoint_auth_methods", md.TokenEndpointAuthMethodsSupported, "none")
	assertContains(t, "token_endpoint_auth_methods", md.TokenEndpointAuthMethodsSupported, "client_secret_basic")
	assertContains(t, "token_endpoint_auth_methods", md.TokenEndpointAuthMethodsSupported, "client_secret_post")
	assertContains(t, "scopes", md.ScopesSupported, auth.ScopeReadOnly)
}

// The handler serves the JSON document on GET and rejects other methods.
func TestAuthorizationServerMetadataHandler(t *testing.T) {
	const issuer = "https://devicechain.example.com/user-management"
	h := AuthorizationServerMetadataHandler(issuer)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetadataPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var md AuthorizationServerMetadata
	if err := json.Unmarshal(rec.Body.Bytes(), &md); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if md.Issuer != issuer {
		t.Errorf("served issuer = %q, want %q", md.Issuer, issuer)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, MetadataPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func assertContains(t *testing.T, field string, got []string, want string) {
	t.Helper()
	for _, v := range got {
		if v == want {
			return
		}
	}
	t.Errorf("%s = %v, want to contain %q", field, got, want)
}
