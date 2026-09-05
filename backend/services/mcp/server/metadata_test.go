// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

func TestProtectedResourceMetadata(t *testing.T) {
	const resource = "https://mcp.example.com"
	const issuer = "https://as.example.com/user-management"
	h := ProtectedResourceMetadataHandler(resource, issuer)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ProtectedResourceMetadataPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var md protectedResourceMetadata
	if err := json.Unmarshal(rec.Body.Bytes(), &md); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if md.Resource != resource {
		t.Errorf("resource = %q, want %q", md.Resource, resource)
	}
	if len(md.AuthorizationServers) != 1 || md.AuthorizationServers[0] != issuer {
		t.Errorf("authorization_servers = %v, want [%s]", md.AuthorizationServers, issuer)
	}
	// Both scopes must be discoverable. A client cannot request what the metadata
	// does not advertise, so an unadvertised `location` would leave query_locations
	// unreachable exactly as the missing ceiling once did.
	for _, want := range []string{coreauth.ScopeReadOnly, coreauth.ScopeLocation} {
		found := false
		for _, s := range md.ScopesSupported {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("scopes_supported %v is missing %q", md.ScopesSupported, want)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ProtectedResourceMetadataPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// The RFC 9728 §3.1 construction rule, case by case.
//
// 🔴 THIS TEST USED TO PIN THE DEFECT. It asserted the metadata URL was the well-known
// path at the resource identifier's ORIGIN, "not appended after any path the resource
// id carries" — which reads like a considered reading of the RFC and is not one. §3.1
// neither appends nor discards: it INSERTS the suffix between the host and the path.
// Discarding the path published a URL that (a) names whatever OTHER resource sits at
// that origin and (b) on a real instance is routed to the console SPA, so following the
// challenge — the discovery step the MCP specification requires of a client — returned
// index.html.
func TestMetadataURL(t *testing.T) {
	for _, tc := range []struct{ resource, want string }{
		// A path is inserted between host and path, never dropped.
		{"https://mcp.example.com/mcp", "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"},
		{"https://iot.example.com/api/mcp", "https://iot.example.com/.well-known/oauth-protected-resource/api/mcp"},
		// The base case: no path, so nothing to insert around.
		{"https://mcp.example.com", "https://mcp.example.com/.well-known/oauth-protected-resource"},
		// A non-default port is part of the origin and must survive.
		{"http://localhost:8080/api/mcp", "http://localhost:8080/.well-known/oauth-protected-resource/api/mcp"},
		// 🔑 NO TRAILING-SLASH CASE HERE, ON PURPOSE. §3.1's slash normalisation is
		// real and coreauth.WellKnownPath implements it — but asserting it HERE would
		// state that ".../api/mcp/" is a usable resource identifier for this server,
		// and it is not: the location normalises the slash away while the document's
		// `resource` field and the token audience keep it, so a client rejects the
		// document it just fetched. config.Validate refuses such an identifier
		// outright (TestValidate), which is the honest place to say so. The
		// normalisation stays pinned where it belongs, in coreauth's own test.
	} {
		if got := metadataURL(tc.resource); got != tc.want {
			t.Errorf("metadataURL(%q) = %q, want %q", tc.resource, got, tc.want)
		}
	}
}

// The local path is the absolute URL minus the origin, and it is a separate function
// because the mux serves it, the 404 under the well-known subtree names it, and
// metadataURL advertises it. All three go through this one call, so they cannot
// disagree about where the document lives.
func TestProtectedResourceMetadataPathFor(t *testing.T) {
	for _, tc := range []struct{ resource, want string }{
		{"https://iot.example.com/api/mcp", "/.well-known/oauth-protected-resource/api/mcp"},
		{"https://mcp.example.com", "/.well-known/oauth-protected-resource"},
		{"::not a url::", "/.well-known/oauth-protected-resource"},
	} {
		if got := ProtectedResourceMetadataPathFor(tc.resource); got != tc.want {
			t.Errorf("ProtectedResourceMetadataPathFor(%q) = %q, want %q", tc.resource, got, tc.want)
		}
	}
}
