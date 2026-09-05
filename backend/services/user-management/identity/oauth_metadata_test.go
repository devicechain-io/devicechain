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
	if md.UserinfoEndpoint != issuer+"/oauth/userinfo" {
		t.Errorf("userinfo_endpoint = %q", md.UserinfoEndpoint)
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
	// A client can only request what discovery advertises, so an unadvertised scope
	// is an unreachable capability: `location` must appear here or nothing can ever
	// be authorized to read a device's position over OAuth.
	assertContains(t, "scopes", md.ScopesSupported, auth.ScopeLocation)
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

// The location a client actually asks for, for the issuer shape this instance
// derives: the RFC 8414 §3.1 path-INSERTED path.
//
// 🔴 IT IS PINNED AS A WHOLE STRING BECAUSE THE THREE PLAUSIBLE ANSWERS ARE ALL
// PATH-SHAPED. Inserted is specified; appended is what the ingress happened to route;
// the bare suffix is what a path-less issuer uses. The MCP reference client tries the
// inserted one first and aborts the walk on the first error, so getting this one
// wrong is not a fallback away from working — it is the end of discovery.
func TestMetadataPathForInsertsTheIssuerPath(t *testing.T) {
	const issuer = "https://iot.example.com/api/user-management"
	if got, want := MetadataPathFor(issuer),
		"/.well-known/oauth-authorization-server/api/user-management"; got != want {
		t.Errorf("MetadataPathFor(%q) = %q, want %q", issuer, got, want)
	}
	// A path-less issuer collapses onto MetadataPath, which is what lets main.go skip
	// the second registration rather than registering the same pattern twice (which
	// ServeMux panics on).
	if got := MetadataPathFor("https://as.example.com"); got != MetadataPath {
		t.Errorf("MetadataPathFor(path-less) = %q, want %q", got, MetadataPath)
	}
}

// The registration, driven through a real mux at the URLs a client actually requests.
//
// 🔴 THIS IS THE HALF THAT WAS MISSING, NOT MetadataPathFor. A function returning the
// right string is worth nothing if nothing mounts the document there, and the mounting
// lived in main.go where no test reaches it. Both locations are exercised, and the
// document is decoded rather than merely counted as a 200 — a mux that answered with
// the wrong handler would pass a status check.
func TestRegisterMetadataHandlersServesBothLocations(t *testing.T) {
	const issuer = "https://iot.example.com/api/user-management"
	mux := http.NewServeMux()
	RegisterMetadataHandlers(mux, issuer)

	for _, path := range []string{
		// What a client builds for a path-carrying issuer, and tries FIRST.
		"/.well-known/oauth-authorization-server/api/user-management",
		// The bare suffix: the location for a path-less issuer, and what the
		// /api/<area> ingress rule delivers for a client that appends instead.
		MetadataPath,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, rec.Code)
			continue
		}
		var md AuthorizationServerMetadata
		if err := json.Unmarshal(rec.Body.Bytes(), &md); err != nil {
			t.Errorf("GET %s: body is not the metadata document: %v", path, err)
			continue
		}
		if md.Issuer != issuer {
			t.Errorf("GET %s: issuer = %q, want %q", path, md.Issuer, issuer)
		}
	}
}

// A path-less issuer must not register the same pattern twice — ServeMux panics on a
// duplicate, which would take the service down at startup rather than at review.
func TestRegisterMetadataHandlersDoesNotDoubleRegisterForAPathlessIssuer(t *testing.T) {
	mux := http.NewServeMux()
	RegisterMetadataHandlers(mux, "https://as.example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetadataPath, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
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
