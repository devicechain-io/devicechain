// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// maxRedirects bounds a redirect chain on a client built by HTTPClient. Supplying a
// CheckRedirect replaces net/http's default, and the default is also what caps the
// chain, so the cap has to be carried here too.
const maxRedirects = 10

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

// ErrTokenLifetimeTooShort is returned (wrapped, naming both durations) by
// AccessTokenValidFor when even a freshly issued access token does not live as long as
// the caller asked: the server's access-token lifetime is shorter than the work the
// token has to outlast.
var ErrTokenLifetimeTooShort = errors.New("the access token does not live long enough")

// AccessTokenValidFor returns a tenant access token that is still valid for at least min
// from now, renewing it first if the cached one would expire sooner.
//
// It is for a caller that presents ONE token for a long time and cannot swap it midway,
// such as a WebSocket whose token is checked at connect and which the server closes when
// that token expires. AccessToken's own margin (refreshSkew) protects a single request;
// it says nothing about a socket held for twenty minutes.
//
// If a freshly issued token still has less than min left, it returns
// ErrTokenLifetimeTooShort rather than a token that is known to expire early.
//
// Renewal shares AccessToken's single-flight, so it never runs a second exchange beside
// one already in flight: two concurrent renewals would spend the SAME refresh token, one
// would lose the server's single-use claim, and that caller would fall back to a full
// sign-in. Joining a flight started by AccessToken can hand back the cached token, good
// enough for that caller but not for min, so a flight that did not issue a token is
// followed by another one that asks for min.
func (s *TenantSession) AccessTokenValidFor(ctx context.Context, min time.Duration) (string, error) {
	if access, exp := s.cached(); access != "" && time.Until(exp) >= min {
		return access, nil
	}
	for range maxJoinedFlights {
		r, err := s.renew(ctx, min)
		if err != nil {
			return "", err
		}
		left := time.Until(r.expiresAt)
		if left >= min {
			return r.token, nil
		}
		if r.fresh {
			return "", fmt.Errorf("%w: a freshly issued one is valid for %s, and %s was needed",
				ErrTokenLifetimeTooShort, left.Round(time.Second), min)
		}
	}
	return "", fmt.Errorf("userclient: no access token valid for %s after %d renewals, each "+
		"joining another caller's", min, maxJoinedFlights)
}

// maxJoinedFlights bounds how many renewals AccessTokenValidFor makes when each one joins
// a flight that some other caller started and that did not issue a token.
const maxJoinedFlights = 3

// renewal is what one renewal flight hands every caller that joined it.
type renewal struct {
	token     string
	expiresAt time.Time
	// fresh is true when the flight issued a token rather than finding the cached one
	// still good enough.
	fresh bool
}

// cached returns the cached access token and its expiry.
func (s *TenantSession) cached() (string, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.access, s.expiresAt
}

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

// HTTPClient returns an *http.Client that attaches the tenant access token as a bearer
// only to requests for allowedHost, and that refuses to follow a redirect off that host
// — for handing to a generated GraphQL client (genqlient) that targets one
// tenant-scoped endpoint.
//
// The pin is what makes the client safe to hand out. The token is attached by a
// RoundTripper, and a RoundTripper runs once per hop, after net/http has already decided
// whether Authorization may be carried across a redirect; without the pin it would put
// the header back on a hop net/http had deliberately sent bare.
//
// allowedHost may be written as a hostname, a host:port or a full endpoint URL: only the
// hostname is compared, case-insensitively. A redirect that changes only the port
// therefore keeps the bearer, which is also net/http's own rule — it compares hostnames
// and ignores the port. A request for any other host is sent without the token, so an
// allowedHost that names no host yields a client that authenticates nothing.
//
// Because the comparison is host-only, a same-host redirect that downgrades the scheme —
// https://api.example.com to http://api.example.com — stays on the pinned host and keeps
// the bearer. That is again net/http's own rule, but it means pinning a public TLS
// endpoint accepts a hop to plaintext on that host: pass a base transport that refuses
// plaintext if that matters to the caller.
func (s *TenantSession) HTTPClient(allowedHost string) *http.Client {
	host := normalizeHost(allowedHost)
	return &http.Client{
		Timeout:   s.httpc.Timeout,
		Transport: &bearerTransport{base: s.httpc.Transport, tok: s.token, host: host},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !hostMatches(host, req.URL) {
				return fmt.Errorf("userclient: refusing to follow a redirect to %s: this client is pinned to %s", req.URL.Host, host)
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("userclient: stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// normalizeHost reduces a hostname, a host:port or a full URL to the lowercase hostname
// to compare against. An IPv6 literal loses its brackets, which is the form url.Hostname
// reports.
//
// Anything that names no host yields "", which pins nothing. That matters because the
// readings below are otherwise happy to return a fragment: "http:///graphql" would be
// read as the host "http", a name a Kubernetes Service can genuinely have. Likewise the
// host:port reading only applies when the port is numeric, so a string carrying userinfo
// ("user:pw@api.example.com") is not read as the host "user" — it is kept whole, and a
// whole string with an "@" in it matches no URL hostname.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		u, err := url.Parse(h)
		if err != nil || u.Host == "" {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	if hostOnly, port, err := net.SplitHostPort(h); err == nil && isNumeric(port) {
		h = hostOnly
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(h)
}

// isNumeric reports whether s is a non-empty run of ASCII digits — a port, as opposed to
// whatever else happened to follow a colon.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// hostMatches reports whether u names the pinned host. A blank pin matches nothing.
func hostMatches(host string, u *url.URL) bool {
	return host != "" && strings.EqualFold(u.Hostname(), host)
}

// token serves a still-valid cached access token under a read lock, or single-flights
// a renewal. The renewal re-checks the cache inside the flight (so a concurrent burst
// collapses to one exchange) and runs on a detached context so one caller's
// cancellation cannot abort a renewal shared by others.
func (s *TenantSession) token(ctx context.Context) (string, error) {
	access, exp := s.cached()
	if access != "" && time.Until(exp) > refreshSkew {
		return access, nil
	}
	r, err := s.renew(ctx, refreshSkew)
	return r.token, err
}

// renew single-flights a renewal unless the cached token already has more than need left
// (re-checked inside the flight, so a concurrent burst collapses to one exchange). Every
// renewal on this session goes through the ONE key "auth": see AccessTokenValidFor.
func (s *TenantSession) renew(ctx context.Context, need time.Duration) (renewal, error) {
	ch := s.group.DoChan("auth", func() (any, error) {
		access, exp := s.cached()
		if access != "" && time.Until(exp) > need {
			return renewal{token: access, expiresAt: exp}, nil
		}
		at, err := s.reauth(context.Background())
		if err != nil {
			return renewal{}, err
		}
		r := renewal{token: at.AccessToken, expiresAt: parseExpiry(at.ExpiresAt), fresh: true}
		s.mu.Lock()
		s.access, s.refreshToken, s.expiresAt = r.token, at.RefreshToken, r.expiresAt
		s.mu.Unlock()
		return r, nil
	})
	select {
	case <-ctx.Done():
		return renewal{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return renewal{}, res.Err
		}
		return res.Val.(renewal), nil
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

// bearerTransport injects a freshly-resolved bearer token on each outbound request for
// host, and leaves a request for any other host alone.
type bearerTransport struct {
	base http.RoundTripper
	tok  func(context.Context) (string, error)
	host string // the one hostname this transport will attach the token to
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		// Unreachable from HTTPClient, whose session client always carries a
		// transport (defaultHTTP fills one in). The fallback names this package's
		// transport rather than http.DefaultTransport so a zero-value
		// bearerTransport cannot quietly reintroduce the process default.
		base = sharedTransport
	}
	if !hostMatches(t.host, req.URL) {
		// Off the pinned host. A redirected hop arrives here with Authorization
		// already dropped by net/http, and setting it again would hand the token to
		// a host it was never minted for. Send the hop as it stands, and acquire no
		// token for it.
		return base.RoundTrip(req)
	}
	token, err := t.tok(req.Context())
	if err != nil {
		return nil, fmt.Errorf("userclient: acquire token: %w", err)
	}
	// Clone so we never mutate the caller's request (net/http contract).
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return base.RoundTrip(clone)
}
