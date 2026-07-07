// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package userclient is the application/integration-plane client for the DeviceChain
// two-tier auth flow (ADR-034): the reusable "RealClient" that any outside process —
// a sim (ADR-035), dcctl, an integration — uses to authenticate as a user identity
// and drive the tenant-scoped GraphQL APIs, without hand-rolling the handshake.
//
// It wraps three user-management operations — login -> selectTenant -> refresh
// (ADR-033) — behind two caching sessions:
//
//   - TenantSession holds a tenant access token, auto-refreshes it shortly before
//     expiry (single-flighted, context-aware), and falls back to a full re-login if
//     the refresh token itself has expired. Use it for tenant-scoped data-plane calls
//     (device-management, event ingress subscribe, etc.). The access token carries
//     its own tenant claim, so no X-DC-Tenant header is needed.
//   - AdminSession holds an identity token (from login) and re-logins on expiry. Use
//     it only for the instance admin surface (/admin/graphql), which requires an
//     identity-tier token plus per-resolver system authorities.
//
// The package deliberately mirrors core/svcclient's envelope/decode/refresh-skew
// shape; the only real difference is the token tier (user two-tier vs. service mint).
package userclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// requestTimeout bounds a single auth exchange or GraphQL round-trip when the
	// caller supplies no http.Client of its own.
	requestTimeout = 10 * time.Second
	// refreshSkew renews a cached token this far ahead of its expiry so a call never
	// rides a token that expires mid-flight.
	refreshSkew = 1 * time.Minute
	// maxResponseBytes caps a response read so a misbehaving peer cannot exhaust memory.
	maxResponseBytes = 1 << 20
)

// Membership is one tenant a logged-in identity belongs to (the login result lists
// them so a caller can choose which tenant to select).
type Membership struct {
	Tenant string   `json:"tenant"`
	Roles  []string `json:"roles"`
}

// IdentityAuth is the result of login: the tenant-less identity token plus the
// identity's memberships. The identity token is what /admin/graphql accepts and what
// selectTenant is exchanged against.
type IdentityAuth struct {
	IdentityToken string       `json:"identityToken"`
	ExpiresAt     string       `json:"expiresAt"`
	Superuser     bool         `json:"superuser"`
	Memberships   []Membership `json:"memberships"`
}

// AuthToken is the result of selectTenant/refresh: a tenant access+refresh pair.
type AuthToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
}

const (
	loginMutation = `mutation($email:String!,$password:String!){` +
		`login(email:$email,password:$password){identityToken expiresAt superuser memberships{tenant roles}}}`
	selectTenantMutation = `mutation($identityToken:String!,$tenant:String!){` +
		`selectTenant(identityToken:$identityToken,tenant:$tenant){accessToken refreshToken expiresAt}}`
	refreshMutation = `mutation($refreshToken:String!){` +
		`refresh(refreshToken:$refreshToken){accessToken refreshToken expiresAt}}`
)

// Login exchanges email+password at user-management's data-plane GraphQL endpoint
// (userGraphQL, tenant-less) for an identity token + memberships.
func Login(ctx context.Context, httpc *http.Client, userGraphQL, email, password string) (IdentityAuth, error) {
	var out struct {
		Login IdentityAuth `json:"login"`
	}
	err := graphqlPost(ctx, httpc, userGraphQL, nil, loginMutation,
		map[string]any{"email": email, "password": password}, &out)
	if err != nil {
		return IdentityAuth{}, err
	}
	if out.Login.IdentityToken == "" {
		return IdentityAuth{}, fmt.Errorf("userclient: login returned an empty identity token")
	}
	return out.Login, nil
}

// SelectTenant exchanges an identity token + a chosen tenant for a tenant access+refresh pair.
func SelectTenant(ctx context.Context, httpc *http.Client, userGraphQL, identityToken, tenant string) (AuthToken, error) {
	var out struct {
		SelectTenant AuthToken `json:"selectTenant"`
	}
	err := graphqlPost(ctx, httpc, userGraphQL, nil, selectTenantMutation,
		map[string]any{"identityToken": identityToken, "tenant": tenant}, &out)
	if err != nil {
		return AuthToken{}, err
	}
	if out.SelectTenant.AccessToken == "" {
		return AuthToken{}, fmt.Errorf("userclient: selectTenant returned an empty access token")
	}
	return out.SelectTenant, nil
}

// Refresh rotates a refresh token into a fresh access+refresh pair. The server
// re-resolves authorities and revokes the old refresh jti, so a replayed refresh
// token cannot mint two live sessions.
func Refresh(ctx context.Context, httpc *http.Client, userGraphQL, refreshToken string) (AuthToken, error) {
	var out struct {
		Refresh AuthToken `json:"refresh"`
	}
	err := graphqlPost(ctx, httpc, userGraphQL, nil, refreshMutation,
		map[string]any{"refreshToken": refreshToken}, &out)
	if err != nil {
		return AuthToken{}, err
	}
	if out.Refresh.AccessToken == "" {
		return AuthToken{}, fmt.Errorf("userclient: refresh returned an empty access token")
	}
	return out.Refresh, nil
}

// graphqlPost executes one GraphQL operation against url, applies any extra headers
// (e.g. Authorization), and decodes the response "data" object into out. A non-2xx
// status or a non-empty GraphQL "errors" array becomes an error. It mirrors
// svcclient's envelope handling so both clients surface failures identically.
func graphqlPost(ctx context.Context, httpc *http.Client, url string, headers map[string]string, query string, variables map[string]any, out any) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("userclient: endpoint URL is required")
	}
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("userclient: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("userclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := defaultHTTP(httpc).Do(req)
	if err != nil {
		return fmt.Errorf("userclient: call %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("userclient: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("userclient: %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("userclient: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("userclient: %s: %s", url, strings.Join(msgs, "; "))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("userclient: decode data: %w", err)
		}
	}
	return nil
}

// parseExpiry reads an RFC3339 expiry string (the authoritative access/identity token
// expiry returned by user-management). A blank or unparseable value yields the zero
// time, which the sessions treat as "already expired" so they refresh eagerly rather
// than ride an unknown-lifetime token.
func parseExpiry(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// defaultHTTP returns httpc if non-nil, else a client bounded by requestTimeout.
func defaultHTTP(httpc *http.Client) *http.Client {
	if httpc != nil {
		return httpc
	}
	return &http.Client{Timeout: requestTimeout}
}
