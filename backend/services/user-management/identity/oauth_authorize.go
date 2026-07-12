// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// AuthorizeParams is the parsed OAuth 2.1 authorization request (RFC 6749 §4.1.1 +
// RFC 7636). Only the authorization-code + PKCE parameters this AS supports are
// modeled; anything else is ignored.
type AuthorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string // RFC 8707 resource indicator (optional) → aud
}

// ParseAuthorizeParams reads the authorize parameters from a query/form value set.
func ParseAuthorizeParams(v url.Values) AuthorizeParams {
	return AuthorizeParams{
		ResponseType:        v.Get("response_type"),
		ClientID:            v.Get("client_id"),
		RedirectURI:         v.Get("redirect_uri"),
		Scope:               v.Get("scope"),
		State:               v.Get("state"),
		CodeChallenge:       v.Get("code_challenge"),
		CodeChallengeMethod: v.Get("code_challenge_method"),
		Resource:            v.Get("resource"),
	}
}

// Authorization-request errors. The two client-identity errors (unknown/disabled
// client, unregistered redirect_uri) are special: because the redirect_uri is not
// yet trusted, the endpoint must render an error page rather than redirect
// (RFC 6749 §4.1.2.1). Every other error is reported by redirecting back to the
// (validated) redirect_uri with an error code.
var (
	// ErrAuthorizeClientUnknown means the client_id is unregistered or disabled.
	ErrAuthorizeClientUnknown = errors.New("unknown or disabled client")
	// ErrAuthorizeRedirectUnregistered means the redirect_uri does not match any of
	// the client's registered URIs.
	ErrAuthorizeRedirectUnregistered = errors.New("redirect_uri is not registered for this client")
)

// authorizeRedirectError carries an RFC 6749 §4.1.2.1 error code the endpoint
// appends to the redirect_uri (only reached once the redirect_uri is trusted).
type authorizeRedirectError struct {
	Code string // e.g. "invalid_request", "invalid_scope", "unsupported_response_type"
	Desc string
}

func (e *authorizeRedirectError) Error() string { return e.Code + ": " + e.Desc }

// ResolveAuthorizeClient looks up the client and verifies the redirect_uri is one
// it registered — the two checks that must pass before any error can be safely sent
// to the redirect_uri. It is deliberately separate from the request-parameter
// validation (which redirects its errors). Returns ErrAuthorizeClientUnknown or
// ErrAuthorizeRedirectUnregistered for the render-an-error-page cases.
func (m *Manager) ResolveAuthorizeClient(ctx context.Context, clientID, redirectURI string) (*iam.OAuthClient, error) {
	client, err := m.iam.OAuthClientByClientId(ctx, clientID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAuthorizeClientUnknown
		}
		return nil, err
	}
	if !client.Enabled {
		return nil, ErrAuthorizeClientUnknown
	}
	if !redirectURIRegistered(client.RedirectURIs, redirectURI) {
		return nil, ErrAuthorizeRedirectUnregistered
	}
	return client, nil
}

// ValidateAuthorizeRequest checks the request parameters against the resolved
// client, AFTER ResolveAuthorizeClient has trusted the redirect_uri. Its errors are
// *authorizeRedirectError (reported by redirecting back). It enforces
// response_type=code, a PKCE S256 challenge (PKCE is mandatory — public clients),
// and that every requested scope is both a scope the AS grants and one the client
// is registered for.
func ValidateAuthorizeRequest(client *iam.OAuthClient, p AuthorizeParams) error {
	if p.ResponseType != "code" {
		return &authorizeRedirectError{"unsupported_response_type", "only response_type=code is supported"}
	}
	if p.CodeChallenge == "" {
		return &authorizeRedirectError{"invalid_request", "code_challenge is required (PKCE is mandatory)"}
	}
	// An absent method defaults to "plain" in RFC 7636, which we do not support, so
	// require S256 explicitly.
	if p.CodeChallengeMethod != "S256" {
		return &authorizeRedirectError{"invalid_request", "code_challenge_method must be S256"}
	}
	if p.Scope == "" {
		return &authorizeRedirectError{"invalid_scope", "scope is required"}
	}
	if !auth.ScopeSupported(p.Scope) {
		return &authorizeRedirectError{"invalid_scope", "requested scope is not supported"}
	}
	if !scopesRegistered(client.Scopes, p.Scope) {
		return &authorizeRedirectError{"invalid_scope", "requested scope is not registered for this client"}
	}
	return nil
}

// IssueAuthorizationCode mints a one-time authorization code for an authenticated,
// consenting user who selected tenant. It verifies the user may act in the tenant
// (a code is never issued for a tenant the subject cannot access — that would only
// fail later at redemption), then stores the code binding for the token endpoint to
// redeem. The resource indicator (RFC 8707), if present, becomes the token audience.
func (m *Manager) IssueAuthorizationCode(ctx context.Context, p AuthorizeParams, email, tenant string) (string, error) {
	if err := m.assertTenantAccess(ctx, email, tenant); err != nil {
		return "", err
	}
	code, err := newAuthorizationCode()
	if err != nil {
		return "", err
	}
	rec := AuthorizationCode{
		ClientId:      p.ClientID,
		RedirectURI:   p.RedirectURI,
		CodeChallenge: p.CodeChallenge,
		Email:         email,
		Tenant:        tenant,
		Scope:         p.Scope,
	}
	if p.Resource != "" {
		rec.Audience = []string{p.Resource}
	}
	if err := m.SaveAuthorizationCode(code, rec); err != nil {
		return "", err
	}
	return code, nil
}

// assertTenantAccess confirms the identity may act in tenant — an enabled member,
// or a superuser (who may break glass into any tenant). Mirrors the SelectTenant
// gate so the authorize step never issues a code the token step would reject.
func (m *Manager) assertTenantAccess(ctx context.Context, email, tenant string) error {
	id, err := m.iam.IdentityByEmail(ctx, email)
	if err != nil || !id.Enabled {
		return ErrInvalidCredentials
	}
	if isSuperuser(id) {
		return nil
	}
	mem := findMembership(id.Memberships, tenant)
	if mem == nil || !mem.Enabled {
		return errTenantAccessDenied
	}
	return nil
}

// newAuthorizationCode returns a 256-bit cryptographically random, URL-safe code.
func newAuthorizationCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating authorization code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// redirectURIRegistered reports whether requested matches one of the client's
// registered redirect URIs under the OAuth 2.1 matching rules.
func redirectURIRegistered(registered []string, requested string) bool {
	for _, r := range registered {
		if redirectURIMatches(r, requested) {
			return true
		}
	}
	return false
}

// redirectURIMatches applies OAuth 2.1 redirect-URI matching: exact string match,
// EXCEPT that for a loopback registration (RFC 8252 §7.3) the port may vary — a
// native app receives the redirect on an ephemeral loopback port it cannot fix at
// registration time. For the loopback case the scheme, host, path, and query must
// still match exactly; only the port is free. Everything else is byte-exact (no
// wildcards, no subpath), so an attacker cannot register a prefix and redirect
// elsewhere.
func redirectURIMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	ru, err1 := url.Parse(registered)
	qu, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil {
		return false
	}
	// The port-free exception applies only when BOTH sides are loopback, so a
	// non-loopback registration can never be matched by varying a port.
	if !isLoopbackHost(ru.Hostname()) || !isLoopbackHost(qu.Hostname()) {
		return false
	}
	return ru.Scheme == qu.Scheme &&
		ru.Hostname() == qu.Hostname() &&
		ru.Path == qu.Path &&
		ru.RawQuery == qu.RawQuery
}

// isLoopbackHost matches the loopback host set ValidateRedirectURI permits for http.
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// scopesRegistered reports whether every scope in requested (space-delimited) is in
// the client's registered scope set.
func scopesRegistered(registered []string, requested string) bool {
	set := make(map[string]struct{}, len(registered))
	for _, s := range registered {
		set[s] = struct{}{}
	}
	for _, s := range auth.ParseScope(requested) {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}
