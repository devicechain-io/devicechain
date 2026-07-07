// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// authStub emulates user-management's data-plane GraphQL for login/selectTenant/refresh,
// routing by operation and handing back incrementing access tokens so a test can tell
// one exchange from the next. selectExpiry controls the access-token lifetime it claims;
// refreshErr makes refresh fail so the fallback-to-login path can be exercised.
type authStub struct {
	mu         sync.Mutex
	logins     int
	selects    int
	refreshes  int
	n          int
	selectExp  string
	refreshErr bool
}

func farFuture() string  { return time.Now().Add(time.Hour).Format(time.RFC3339) }
func nearExpiry() string { return time.Now().Add(30 * time.Second).Format(time.RFC3339) }

func (s *authStub) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(body.Query, "login("):
		s.logins++
		writeData(w, fmt.Sprintf(
			`{"login":{"identityToken":"id-tok","expiresAt":%q,"superuser":true,"memberships":[{"tenant":"acme","roles":["tenant-admin"]}]}}`,
			farFuture()))
	case strings.Contains(body.Query, "selectTenant("):
		s.selects++
		s.n++
		writeData(w, fmt.Sprintf(`{"selectTenant":{"accessToken":"access-%d","refreshToken":"refresh-%d","expiresAt":%q}}`, s.n, s.n, s.selectExp))
	case strings.Contains(body.Query, "refresh("):
		s.refreshes++
		if s.refreshErr {
			writeErrors(w, "refresh token expired")
			return
		}
		s.n++
		writeData(w, fmt.Sprintf(`{"refresh":{"accessToken":"access-%d","refreshToken":"refresh-%d","expiresAt":%q}}`, s.n, s.n, farFuture()))
	default:
		writeErrors(w, "unknown operation")
	}
}

func writeData(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"data":%s}`, data)
}

func writeErrors(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, msg)
}

func TestExchangeOperations(t *testing.T) {
	stub := &authStub{selectExp: farFuture()}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	ctx := context.Background()

	ia, err := Login(ctx, nil, srv.URL, "u@x", "pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if ia.IdentityToken != "id-tok" || !ia.Superuser || len(ia.Memberships) != 1 {
		t.Fatalf("unexpected identity auth: %+v", ia)
	}
	at, err := SelectTenant(ctx, nil, srv.URL, ia.IdentityToken, "acme")
	if err != nil {
		t.Fatalf("selectTenant: %v", err)
	}
	if at.AccessToken == "" || at.RefreshToken == "" {
		t.Fatalf("unexpected auth token: %+v", at)
	}
	if _, err := Refresh(ctx, nil, srv.URL, at.RefreshToken); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

// A valid cached token is reused: the first AccessToken logs in + selects a tenant, the
// second returns from cache without touching the server.
func TestTenantSessionCachesToken(t *testing.T) {
	stub := &authStub{selectExp: farFuture()}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	first, err := s.AccessToken(ctx)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.AccessToken(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Fatalf("cached token must be reused: %q != %q", first, second)
	}
	if stub.logins != 1 || stub.selects != 1 {
		t.Fatalf("expected exactly one login+select, got logins=%d selects=%d", stub.logins, stub.selects)
	}
}

// When the cached access token is within the refresh skew of expiry, the next call
// renews via the refresh token (not a fresh login).
func TestTenantSessionRefreshesBeforeExpiry(t *testing.T) {
	stub := &authStub{selectExp: nearExpiry()}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	first, _ := s.AccessToken(ctx) // login + selectTenant -> access-1 (near expiry)
	second, err := s.AccessToken(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Fatalf("expected a renewed token, got the same: %q", first)
	}
	if stub.refreshes != 1 {
		t.Fatalf("expected exactly one refresh, got %d", stub.refreshes)
	}
	if stub.logins != 1 {
		t.Fatalf("renewal must use refresh, not re-login; logins=%d", stub.logins)
	}
}

// When the refresh token is rejected (expired/revoked), the session falls back to a
// full re-login rather than erroring.
func TestTenantSessionFallsBackToLogin(t *testing.T) {
	stub := &authStub{selectExp: nearExpiry(), refreshErr: true}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	_, _ = s.AccessToken(ctx) // login + select -> access-1 (near)
	second, err := s.AccessToken(ctx)
	if err != nil {
		t.Fatalf("fallback re-login must succeed, got: %v", err)
	}
	if second == "" {
		t.Fatalf("expected a token from re-login")
	}
	if stub.refreshes != 1 || stub.logins != 2 || stub.selects != 2 {
		t.Fatalf("expected refresh(1)->fallback login(2)+select(2), got refresh=%d login=%d select=%d",
			stub.refreshes, stub.logins, stub.selects)
	}
}

func TestAdminSessionCachesAndReportsSuperuser(t *testing.T) {
	stub := &authStub{selectExp: farFuture()}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	a := NewAdminSession(nil, srv.URL, "root@x", "pw")
	ctx := context.Background()

	su, err := a.Superuser(ctx)
	if err != nil {
		t.Fatalf("superuser: %v", err)
	}
	if !su {
		t.Fatalf("expected superuser")
	}
	if _, err := a.token(ctx); err != nil { // second use: cached
		t.Fatalf("token: %v", err)
	}
	if stub.logins != 1 {
		t.Fatalf("admin identity token must be cached; logins=%d", stub.logins)
	}
}

// Query/HTTPClient attach the bearer to the target endpoint.
func TestSessionAttachesBearer(t *testing.T) {
	stub := &authStub{selectExp: farFuture()}
	auth := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer auth.Close()

	var gotAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeData(w, `{"ok":true}`)
	}))
	defer target.Close()

	s := NewTenantSession(nil, auth.URL, "u@x", "pw", "acme")
	var out struct {
		Ok bool `json:"ok"`
	}
	if err := s.Query(context.Background(), target.URL, `query{ok}`, nil, &out); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Bearer access-") {
		t.Fatalf("expected bearer access token, got %q", gotAuth)
	}
	if !out.Ok {
		t.Fatalf("expected decoded data")
	}
}
