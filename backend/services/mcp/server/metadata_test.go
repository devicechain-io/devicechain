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
	found := false
	for _, s := range md.ScopesSupported {
		if s == coreauth.ScopeReadOnly {
			found = true
		}
	}
	if !found {
		t.Errorf("scopes_supported %v missing read-only", md.ScopesSupported)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ProtectedResourceMetadataPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestMetadataURL(t *testing.T) {
	// The metadata URL is the well-known path at the resource identifier's ORIGIN,
	// not appended after any path the resource id carries.
	if got := metadataURL("https://mcp.example.com/mcp"); got != "https://mcp.example.com"+ProtectedResourceMetadataPath {
		t.Errorf("metadataURL = %q", got)
	}
	if got := metadataURL("https://mcp.example.com"); got != "https://mcp.example.com"+ProtectedResourceMetadataPath {
		t.Errorf("metadataURL = %q", got)
	}
}
