// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// TenantSession authenticates as a user identity into one tenant and keeps a valid
// tenant access token available for data-plane calls. It is safe for concurrent use.
// Authentication is lazy (on first token use) and self-healing: it renews via the
// refresh token shortly before expiry, and falls back to a full re-login if the
// refresh token has itself expired (e.g. a sim idle past the 7-day refresh TTL).
type TenantSession struct {
	httpc       *http.Client
	userGraphQL string // user-management data-plane /graphql (login/selectTenant/refresh)
	email       string
	password    string
	tenant      string

	group singleflight.Group

	mu           sync.RWMutex
	access       string
	refreshToken string
	expiresAt    time.Time
}

// NewTenantSession builds a session that logs in as email/password and selects
// tenant. userGraphQL is the user-management data-plane GraphQL URL; httpc may be nil
// (a default-timeout client is used). No network call happens until the first token
// is needed.
func NewTenantSession(httpc *http.Client, userGraphQL, email, password, tenant string) *TenantSession {
	return &TenantSession{
		httpc:       defaultHTTP(httpc),
		userGraphQL: userGraphQL,
		email:       email,
		password:    password,
		tenant:      tenant,
	}
}

// Tenant returns the tenant this session is scoped to.
func (s *TenantSession) Tenant() string { return s.tenant }

// AccessToken returns a currently-valid tenant access token, authenticating or
// refreshing as needed. Use it for the graphql-ws connectionParams bearer.
func (s *TenantSession) AccessToken(ctx context.Context) (string, error) { return s.token(ctx) }

// Query executes a GraphQL operation against baseURL (a tenant-scoped service GraphQL
// endpoint) with the tenant access token as bearer, decoding "data" into out. The
// access token carries its own tenant claim, so no tenant header is set.
func (s *TenantSession) Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error {
	token, err := s.token(ctx)
	if err != nil {
		return err
	}
	return graphqlPost(ctx, s.httpc, baseURL, map[string]string{"Authorization": "Bearer " + token}, query, variables, out)
}

// HTTPClient returns an *http.Client that injects the tenant access token as a bearer
// on every request — for handing to a generated GraphQL client (genqlient) that
// targets tenant-scoped endpoints.
func (s *TenantSession) HTTPClient() *http.Client {
	return &http.Client{
		Timeout:   s.httpc.Timeout,
		Transport: &bearerTransport{base: s.httpc.Transport, tok: s.token},
	}
}

// token serves a still-valid cached access token under a read lock, or single-flights
// a renewal. The renewal re-checks the cache inside the flight (so a concurrent burst
// collapses to one exchange) and runs on a detached context so one caller's
// cancellation cannot abort a renewal shared by others.
func (s *TenantSession) token(ctx context.Context) (string, error) {
	s.mu.RLock()
	access, exp := s.access, s.expiresAt
	s.mu.RUnlock()
	if access != "" && time.Now().Before(exp.Add(-refreshSkew)) {
		return access, nil
	}

	ch := s.group.DoChan("auth", func() (any, error) {
		s.mu.RLock()
		access, exp := s.access, s.expiresAt
		s.mu.RUnlock()
		if access != "" && time.Now().Before(exp.Add(-refreshSkew)) {
			return access, nil
		}
		at, err := s.reauth(context.Background())
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.access, s.refreshToken, s.expiresAt = at.AccessToken, at.RefreshToken, parseExpiry(at.ExpiresAt)
		s.mu.Unlock()
		return at.AccessToken, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	}
}

// reauth renews the tenant token: it tries the refresh token first, and on any failure
// (or when none is held yet) performs a full login -> selectTenant. This keeps a
// long-lived sim alive across an expired refresh token without operator intervention.
func (s *TenantSession) reauth(ctx context.Context) (AuthToken, error) {
	s.mu.RLock()
	refresh := s.refreshToken
	s.mu.RUnlock()

	if refresh != "" {
		if at, err := Refresh(ctx, s.httpc, s.userGraphQL, refresh); err == nil {
			return at, nil
		}
		// Refresh token expired/revoked — fall through to a full re-login.
	}
	ia, err := Login(ctx, s.httpc, s.userGraphQL, s.email, s.password)
	if err != nil {
		return AuthToken{}, err
	}
	return SelectTenant(ctx, s.httpc, s.userGraphQL, ia.IdentityToken, s.tenant)
}

// AdminSession holds an identity-tier token for the instance admin surface
// (/admin/graphql). It re-logins on expiry. It is safe for concurrent use. Only a
// caller whose identity carries the required system authorities (e.g. a superuser)
// will pass the admin resolvers' authorization gates.
type AdminSession struct {
	httpc       *http.Client
	userGraphQL string
	email       string
	password    string

	group singleflight.Group

	mu        sync.RWMutex
	identity  string
	expiresAt time.Time
	superuser bool
}

// NewAdminSession builds an admin session for email/password. userGraphQL is the
// user-management data-plane GraphQL URL used for login. Lazy: no call until first use.
func NewAdminSession(httpc *http.Client, userGraphQL, email, password string) *AdminSession {
	return &AdminSession{
		httpc:       defaultHTTP(httpc),
		userGraphQL: userGraphQL,
		email:       email,
		password:    password,
	}
}

// Query executes a GraphQL operation against adminBaseURL (the /admin/graphql
// endpoint) with the identity token as bearer, decoding "data" into out.
func (a *AdminSession) Query(ctx context.Context, adminBaseURL, query string, variables map[string]any, out any) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	return graphqlPost(ctx, a.httpc, adminBaseURL, map[string]string{"Authorization": "Bearer " + token}, query, variables, out)
}

// Superuser reports whether the last login authenticated a superuser. It forces a
// login if none has happened yet, so callers can fail fast when the admin identity
// lacks the powers the admin surface requires.
func (a *AdminSession) Superuser(ctx context.Context) (bool, error) {
	if _, err := a.token(ctx); err != nil {
		return false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.superuser, nil
}

// token serves a cached identity token or single-flights a login, mirroring
// TenantSession.token.
func (a *AdminSession) token(ctx context.Context) (string, error) {
	a.mu.RLock()
	tok, exp := a.identity, a.expiresAt
	a.mu.RUnlock()
	if tok != "" && time.Now().Before(exp.Add(-refreshSkew)) {
		return tok, nil
	}

	ch := a.group.DoChan("login", func() (any, error) {
		a.mu.RLock()
		tok, exp := a.identity, a.expiresAt
		a.mu.RUnlock()
		if tok != "" && time.Now().Before(exp.Add(-refreshSkew)) {
			return tok, nil
		}
		ia, err := Login(context.Background(), a.httpc, a.userGraphQL, a.email, a.password)
		if err != nil {
			return "", err
		}
		a.mu.Lock()
		a.identity, a.expiresAt, a.superuser = ia.IdentityToken, parseExpiry(ia.ExpiresAt), ia.Superuser
		a.mu.Unlock()
		return ia.IdentityToken, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	}
}

// bearerTransport injects a freshly-resolved bearer token on each outbound request.
type bearerTransport struct {
	base http.RoundTripper
	tok  func(context.Context) (string, error)
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tok(req.Context())
	if err != nil {
		return nil, fmt.Errorf("userclient: acquire token: %w", err)
	}
	// Clone so we never mutate the caller's request (net/http contract).
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
