// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/golang-jwt/jwt/v5"
)

// claimsFor builds an access-token claim set the way IssueOAuthAccess would.
func claimsFor(email, tenant string, sudo bool) *auth.Claims {
	return &auth.Claims{
		Tenant: tenant, Username: email, Email: email, ActingAsSuperuser: sudo,
		Scope: auth.ScopeReadOnly, TokenType: auth.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{Subject: email},
	}
}

func getUserinfo(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, UserinfoPath, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A valid access token yields the subject identity and, for an operator, sudo=true —
// the claim Grafana maps with role_attribute_path to gate cross-tenant metrics.
func TestUserinfoHandler_SuperuserAndTenantUser(t *testing.T) {
	h := UserinfoHandler(func(tok string) (*auth.Claims, error) {
		switch tok {
		case "op":
			return claimsFor("root@dc.local", "acme", true), nil
		case "user":
			return claimsFor("alice@acme.io", "acme", false), nil
		}
		return nil, errors.New("unknown")
	})

	// Operator token → sudo:true, identity fields populated.
	rec := getUserinfo(h, "op")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body userinfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body.Sub != "root@dc.local" || body.Email != "root@dc.local" ||
		body.PreferredUsername != "root@dc.local" || !body.Sudo || body.Tenant != "acme" {
		t.Errorf("operator userinfo = %+v", body)
	}

	// A tenant user → sudo:false (Grafana's role_attribute_path denies them).
	rec = getUserinfo(h, "user")
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Sudo {
		t.Errorf("tenant user sudo = true, want false")
	}
	if body.Sub != "alice@acme.io" {
		t.Errorf("sub = %q, want alice@acme.io", body.Sub)
	}
}

// No Bearer credential → 401 with a bare Bearer challenge; the validator is never
// consulted.
func TestUserinfoHandler_MissingToken(t *testing.T) {
	consulted := false
	h := UserinfoHandler(func(string) (*auth.Claims, error) {
		consulted = true
		return nil, nil
	})
	rec := getUserinfo(h, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if wa := rec.Header().Get("WWW-Authenticate"); wa != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", wa)
	}
	if consulted {
		t.Error("validator must not run without a token")
	}
}

// An invalid/expired token → 401 invalid_token, and the parse error is not echoed.
func TestUserinfoHandler_InvalidToken(t *testing.T) {
	h := UserinfoHandler(func(string) (*auth.Claims, error) {
		return nil, errors.New("token type \"refresh\" does not match expected \"access\"")
	})
	rec := getUserinfo(h, "bad")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if wa := rec.Header().Get("WWW-Authenticate"); wa != `Bearer error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q", wa)
	}
	if b := rec.Body.String(); strings.Contains(b, "refresh") || strings.Contains(b, "expected") {
		t.Errorf("response leaked the parse error: %q", b)
	}
}

func TestUserinfoHandler_MethodNotAllowed(t *testing.T) {
	h := UserinfoHandler(func(string) (*auth.Claims, error) { return claimsFor("a@b.c", "t", false), nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, UserinfoPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", rec.Code)
	}
}
