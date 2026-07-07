// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// echoRoot reports the request's resolved tenant and whether its claims grant
// device:read, so an end-to-end test can prove ServeHTTP stamped both from a
// service token.
type echoRoot struct{}

func (*echoRoot) WhoAmI(ctx context.Context) string {
	tenant, _ := core.TenantFromContext(ctx)
	claims, _ := auth.ClaimsFromContext(ctx)
	return fmt.Sprintf("%s:%v", tenant, claims != nil && claims.HasAuthority(auth.DeviceRead))
}

// End-to-end through ServeHTTP: a service token + X-DC-Tenant header must reach a
// resolver with the tenant stamped from the header and the authorities from the
// token. This exercises the real r.Header.Get resolver + core.WithTenant wiring the
// unit test bypasses.
func TestServeHTTP_ServiceTokenEndToEnd(t *testing.T) {
	iss, v := testIssuerValidator(t)
	gate := core.NewReadinessGate()
	gate.MarkReady(v)
	schema := MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{})
	srv := httptest.NewServer(NewHttpHandler(schema, map[ContextKey]interface{}{}, gate))
	defer srv.Close()

	st, err := iss.IssueService("command-delivery", []string{string(auth.DeviceRead)}, "jti-svc")
	if err != nil {
		t.Fatalf("IssueService: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"query": "query{whoAmI}"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+st.Token)
	req.Header.Set(auth.ServiceTenantHeader, "tenant-a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Data struct{ WhoAmI string } `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, raw)
	}
	if out.Data.WhoAmI != "tenant-a:true" {
		t.Fatalf("service token did not stamp tenant+authorities end-to-end: %q", out.Data.WhoAmI)
	}
}

// The request-body ceiling (ADR-029) is enforced before the body is fully read: a
// body over the limit is rejected (400) rather than buffered into memory. A body
// under the limit runs normally.
func TestServeHTTP_BodySizeCeiling(t *testing.T) {
	t.Setenv(EnvGraphQLMaxBodyBytes, "128")
	schema := MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{})
	srv := httptest.NewServer(NewHttpHandler(schema, map[ContextKey]interface{}{}, nil))
	defer srv.Close()

	post := func(body []byte) int {
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	small, _ := json.Marshal(map[string]string{"query": "query{whoAmI}"})
	if code := post(small); code != http.StatusOK {
		t.Fatalf("a small body was rejected: code=%d", code)
	}
	// Pad the variables past the 128-byte ceiling; the decode must fail on size.
	big, _ := json.Marshal(map[string]any{"query": "query{whoAmI}", "variables": map[string]string{"pad": strings.Repeat("x", 256)}})
	if code := post(big); code != http.StatusBadRequest {
		t.Fatalf("an over-size body was not rejected: code=%d", code)
	}
}

// Neither identity nor refresh tokens are accepted on the data plane.
func TestAuthenticateDataPlane_RejectsOtherTiers(t *testing.T) {
	iss, v := testIssuerValidator(t)
	idt, err := iss.IssueIdentity("a@b.c", nil, []string{string(auth.AuthorityAll)}, "jti-id")
	if err != nil {
		t.Fatalf("IssueIdentity: %v", err)
	}
	rt, err := iss.IssueRefresh("tenant-a", "alice", nil, nil, "jti-r")
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
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
