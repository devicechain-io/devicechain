// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The RS middleware in server.New must gate every MCP request: no token → 401 with
// an RFC 9728 challenge; a token bound to the wrong audience → 401; a token bound to
// this resource but WITHOUT the read-only scope → 403. This pins the whole auth
// wiring (verifier + audience + scope option) so none of it can be silently removed.
func TestServerNew_AuthMiddlewareGatesRequests(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	mcpHandler, _ := New(testResource, "https://as.example.com", validator)
	ts := httptest.NewServer(mcpHandler)
	defer ts.Close()

	post := func(authHeader string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return resp
	}

	// No token → 401, and the WWW-Authenticate challenge points at the RFC 9728
	// protected-resource metadata so a client can discover the AS.
	resp := post("")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}
	if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "resource_metadata") {
		t.Errorf("WWW-Authenticate missing resource_metadata: %q", wa)
	}
	resp.Body.Close()

	// A token bound to a different audience → 401 (anti-confused-deputy).
	wrongAud, _ := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{"https://some-other-resource"}, false, "mcp", "j-aud")
	resp = post("Bearer " + wrongAud.Token)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong aud: status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct audience but a non-read-only scope → 403 (the Scopes option enforced).
	badScope, _ := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
		"some-other-scope", []string{testResource}, false, "mcp", "j-scope")
	resp = post("Bearer " + badScope.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong scope: status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// callerToken fails closed when the request carries no verified token info.
func TestCallerToken_FailsClosed(t *testing.T) {
	if _, _, err := callerToken(&mcp.CallToolRequest{}); err == nil {
		t.Errorf("nil Extra should fail closed")
	}
	if _, _, err := callerToken(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}); err == nil {
		t.Errorf("nil TokenInfo should fail closed")
	}
}
