// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Single sign-on on the MCP authorize page.
//
// The page keeps no server session and no cookie of its own, so the pending authorization
// request (client, redirect URI, PKCE challenge, scope, resource, state) is parked in the
// SAME state record the console's sign-in uses, with kind "authorize". The provider's
// redirect is therefore handled by the one callback that already exists, and what comes back
// to the browser is a single-use resume code and nothing else: the request itself is read
// from the server's record, never from the address the browser returns to, and is
// re-resolved (client still registered and enabled, redirect still registered, scope still
// allowed) before the person is shown a consent screen. Consent is never skipped.
//
// The browser that begins is bound to the resume by a secret it holds in a short-lived,
// HttpOnly cookie scoped to the authorize path; the record stores only its hash. A resume
// link followed in a different browser therefore finds no cookie and completes nothing.

// authorizeResumeParam is the one query parameter a resume carries.
const authorizeResumeParam = "sso"

// AuthorizeBrowserCookie is the cookie that binds the beginning browser to its resume.
const AuthorizeBrowserCookie = "dc_authz_sso"

// AuthorizeSsoBegin is what the authorize page does after a begin: send the browser to the
// provider, having set the binding cookie.
type AuthorizeSsoBegin struct {
	// AuthorizationURL is the provider's, built from the approved snapshot.
	AuthorizationURL string
	// BrowserSecret is the cookie's value. It is never logged and never leaves the cookie.
	BrowserSecret string
	// CookiePath is the path the cookie must be scoped to: the authorize endpoint as the
	// browser reaches it (the public issuer's path included).
	CookiePath string
	// Secure is whether the cookie carries the Secure attribute (the issuer is https).
	Secure bool
}

// AuthorizeResume is a resumed authorize request, ready for the consent screen.
type AuthorizeResume struct {
	Params   AuthorizeParams
	Client   *iam.OAuthClient
	Identity *IdentityAuth
	// Email is the address that signed in, shown on the consent screen so a person who was
	// walked into someone else's sign-in can see whose it is.
	Email string
}

// authorizeReturnURL is where the callback sends the browser back to: the authorize
// endpoint on the issuer's own origin. It is computed, never taken from a request.
func (m *Manager) authorizeReturnURL() (string, error) {
	u, err := url.Parse(m.issuerUrl)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ErrSsoUnavailable
	}
	return strings.TrimSuffix(u.String(), "/") + AuthorizePath, nil
}

// BeginAuthorizeSso starts a sign-in for a pending authorization request that the caller has
// already resolved and validated. email picks the connection by its verified domain. It
// returns (nil, nil) when no connection serves that address (the person signs in some other
// way), ErrSsoUnavailable when one is found but cannot be used, and ErrSsoRateLimited over
// the allowance.
func (m *Manager) BeginAuthorizeSso(ctx context.Context, email string, p AuthorizeParams, source string) (*AuthorizeSsoBegin, error) {
	if !m.SsoReady() {
		return nil, ErrSsoUnavailable
	}
	if !m.ssoLimiter.allow(source) {
		m.countSso("unknown", SsoOutcomeRateLimited)
		return nil, ErrSsoRateLimited
	}
	if len(email) > 256 || email == "" {
		return nil, ErrSsoInvalidRequest
	}
	for _, f := range []string{p.ResponseType, p.ClientID, p.RedirectURI, p.Scope, p.State, p.CodeChallenge, p.CodeChallengeMethod, p.Resource} {
		if len(f) > maxSsoField {
			return nil, ErrSsoInvalidRequest
		}
	}
	returnTo, err := m.authorizeReturnURL()
	if err != nil {
		return nil, err
	}
	conn, err := m.connectionForEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrSsoInvalidRequest) {
			return nil, err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		log.Error().Err(err).Msg("The authorize page could not resolve its single sign-on connection.")
		return nil, ErrSsoUnavailable
	}
	secret, err := randomKey()
	if err != nil {
		return nil, ErrSsoUnavailable
	}
	sum := sha256.Sum256([]byte(secret))
	pending := p
	begin, err := m.beginWithState(ctx, conn, ssoState{
		Kind: ssoKindAuthorize, ReturnTo: returnTo,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), Authorize: &pending,
	})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(returnTo)
	return &AuthorizeSsoBegin{
		AuthorizationURL: begin.AuthorizationURL,
		BrowserSecret:    secret,
		CookiePath:       u.Path,
		Secure:           u.Scheme == "https",
	}, nil
}

// ResumeAuthorizeSso redeems a resume code with the beginning browser's secret. It consumes
// the code first, accepts only an authorize-kind record, re-resolves the client and request
// from the record's pending parameters, and only then starts the session -- so a client
// deleted or re-registered mid-flow ends in an error and leaves no session behind. Every
// failure of the code itself is ErrSsoCodeInvalid.
func (m *Manager) ResumeAuthorizeSso(ctx context.Context, code, browserSecret string) (*AuthorizeResume, error) {
	if !m.SsoReady() {
		return nil, ErrSsoUnavailable
	}
	if len(code) > 128 || len(browserSecret) < 43 || len(browserSecret) > 128 {
		return nil, ErrSsoCodeInvalid
	}
	raw, ok, err := consumeKV(m.ssoCodesKV, code)
	if err != nil {
		log.Error().Err(err).Msg("The single sign-on code store could not be read.")
		return nil, ErrSsoCodeInvalid
	}
	if !ok {
		return nil, ErrSsoCodeInvalid
	}
	var rec ssoCode
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Kind != ssoKindAuthorize || rec.Authorize == nil {
		return nil, ErrSsoCodeInvalid
	}
	if !verifyPKCE(browserSecret, rec.CodeChallenge) {
		return nil, ErrSsoCodeInvalid
	}
	if time.Since(rec.Anchor) > SsoCodeTTL+10*time.Second {
		return nil, ErrSsoCodeInvalid
	}

	p := *rec.Authorize
	client, err := m.ResolveAuthorizeClient(ctx, p.ClientID, p.RedirectURI)
	if err != nil {
		return nil, err
	}
	if verr := ValidateAuthorizeRequest(client, p); verr != nil {
		return nil, verr
	}
	ident, err := m.completeSsoRecord(ctx, rec)
	if err != nil {
		return nil, err
	}
	return &AuthorizeResume{Params: p, Client: client, Identity: ident, Email: rec.Email}, nil
}
