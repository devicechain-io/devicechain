// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// testIssuerValidator builds a matched issuer/validator over a fresh keypair.
func testIssuerValidator(t *testing.T) (*auth.Issuer, *auth.Validator) {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return auth.NewIssuer(key, "test", time.Minute, time.Hour), auth.NewValidator(&key.PublicKey)
}

// A verified service token is admitted on the data plane with its tenant taken
// from the ServiceTenantHeader; a transport that supplies no such header (the WS
// path passes an empty resolver) makes the service token inapplicable and rejected.
func TestAuthenticateDataPlane_ServiceToken(t *testing.T) {
	iss, v := testIssuerValidator(t)
	st, err := iss.IssueService("command-delivery", []string{string(auth.DeviceRead)}, "jti-svc")
	if err != nil {
		t.Fatalf("IssueService: %v", err)
	}

	claims, tenant, err := authenticateDataPlane(v, st.Token, func(h string) string {
		if h == auth.ServiceTenantHeader {
			return "tenant-a"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("data plane rejected a valid service token: %v", err)
	}
	if tenant != "tenant-a" {
		t.Fatalf("service token tenant not taken from header: %q", tenant)
	}
	if !claims.HasAuthority(auth.DeviceRead) {
		t.Fatalf("service token authorities lost: %+v", claims.Authorities)
	}

	if _, _, err := authenticateDataPlane(v, st.Token, func(string) string { return "" }); err == nil {
		t.Fatal("service token accepted with no tenant header (e.g. on the WS transport)")
	}
}

// An access token's tenant always comes from its signed claim — a spoofed
// ServiceTenantHeader must never override it (the header is honored only for the
// service tier, and only after signature verification).
func TestAuthenticateDataPlane_AccessTokenTenantFromClaimNotHeader(t *testing.T) {
	iss, v := testIssuerValidator(t)
	at, err := iss.IssueAccess("tenant-b", "alice", nil, []string{string(auth.DeviceRead)}, "jti-a")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	_, tenant, err := authenticateDataPlane(v, at.Token, func(string) string { return "attacker-tenant" })
	if err != nil {
		t.Fatalf("data plane rejected a valid access token: %v", err)
	}
	if tenant != "tenant-b" {
		t.Fatalf("access-token tenant must come from the signed claim, got %q", tenant)
	}
}

// Neither identity nor refresh tokens are accepted on the data plane.
func TestAuthenticateDataPlane_RejectsOtherTiers(t *testing.T) {
	iss, v := testIssuerValidator(t)
	idt, _ := iss.IssueIdentity("a@b.c", nil, []string{string(auth.AuthorityAll)}, "jti-id")
	rt, _ := iss.IssueRefresh("tenant-a", "alice", nil, nil, "jti-r")
	for name, tok := range map[string]string{"identity": idt.Token, "refresh": rt.Token} {
		if _, _, err := authenticateDataPlane(v, tok, func(string) string { return "tenant-a" }); err == nil {
			t.Fatalf("data plane accepted a %s token", name)
		}
	}
}

// While the readiness gate is closed the validator is nil, so a presented bearer
// token is rejected with 401 rather than being silently trusted or passed
// through unauthenticated (ADR-022 decision 3 / ADR-015 fail-closed).
func TestHandlerRejectsTokenWhileGateClosed(t *testing.T) {
	h := NewHttpHandler(nil, map[ContextKey]interface{}{}, core.NewReadinessGate())

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// With no gate at all (deliberately unauthenticated server) a presented token is
// likewise rejected rather than trusted.
func TestHandlerRejectsTokenWithNoGate(t *testing.T) {
	h := NewHttpHandler(nil, map[ContextKey]interface{}{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// The admin handler (identity policy) requires a token: unlike the data plane it
// does not allow an absent token through, since the admin API has no open entry
// points (ADR-033).
func TestAdminHandlerRequiresToken(t *testing.T) {
	h := NewAdminHttpHandler(nil, map[ContextKey]interface{}{}, core.NewReadinessGate())

	req := httptest.NewRequest(http.MethodPost, "/admin/graphql", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A token presented to the admin handler while the gate is closed (nil validator)
// is rejected rather than trusted, same fail-closed posture as the data plane.
func TestAdminHandlerRejectsTokenWhileGateClosed(t *testing.T) {
	h := NewAdminHttpHandler(nil, map[ContextKey]interface{}{}, core.NewReadinessGate())

	req := httptest.NewRequest(http.MethodPost, "/admin/graphql", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
