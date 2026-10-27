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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/httptransport"
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
	verifyMfaMutation = `mutation($challenge:String!,$code:String!){` +
		`verifyMfa(challenge:$challenge,code:$code){identityToken expiresAt superuser memberships{tenant roles}}}`
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

// codeMfaChallenge is the extensions.code login answers with for an identity that has a second
// factor: the password was right, no token was minted, and the challenge completes the sign-in.
const codeMfaChallenge = "MFA_CHALLENGE"

// MfaRequiredError is Login's answer for an identity with a second factor. It is not a failure of
// the password: the password was right, and the sign-in completes with VerifyMfa (or LoginWithOTP)
// and a code. A caller that cannot supply a code must surface this, loudly, and not sign in again:
// a second Login mints a second challenge and gets the same answer.
type MfaRequiredError struct {
	// Challenge is the handle VerifyMfa takes. It is single-use and short-lived.
	Challenge string
	// ExpiresAt is when the challenge stops being accepted (zero if the server sent none).
	ExpiresAt time.Time
	// Methods is what the server will accept as the code: "TOTP" and "RECOVERY_CODE".
	Methods []string
}

// Error names what the person has to do, since this is the text a command-line user sees.
func (e *MfaRequiredError) Error() string {
	return "userclient: this account requires a second factor; supply a one-time code (or a recovery code)"
}

// OTPFunc supplies the second-factor code for a challenge: from a flag, a prompt, a vault. It is
// called at most once per Login and may block; ctx is the login's.
type OTPFunc func(ctx context.Context, challenge *MfaRequiredError) (string, error)

// VerifyMfa completes a Login that answered with *MfaRequiredError: challenge plus a TOTP code (or a
// recovery code). Its refusals are *GraphQLError; an expired, spent or voided challenge carries
// code MFA_CHALLENGE_EXPIRED and needs a new Login.
func VerifyMfa(ctx context.Context, httpc *http.Client, userGraphQL, challenge, code string) (IdentityAuth, error) {
	var out struct {
		VerifyMfa IdentityAuth `json:"verifyMfa"`
	}
	err := graphqlPost(ctx, httpc, userGraphQL, nil, verifyMfaMutation,
		map[string]any{"challenge": challenge, "code": code}, &out)
	if err != nil {
		return IdentityAuth{}, err
	}
	if out.VerifyMfa.IdentityToken == "" {
		return IdentityAuth{}, fmt.Errorf("userclient: verifyMfa returned an empty identity token")
	}
	return out.VerifyMfa, nil
}

// LoginWithOTP is Login that finishes a second-factor sign-in with otp. With a nil otp it is Login:
// an account with a second factor gets *MfaRequiredError. An error from otp is returned as it is.
func LoginWithOTP(ctx context.Context, httpc *http.Client, userGraphQL, email, password string, otp OTPFunc) (IdentityAuth, error) {
	ia, err := Login(ctx, httpc, userGraphQL, email, password)
	var mfa *MfaRequiredError
	if err == nil || otp == nil || !errors.As(err, &mfa) {
		return ia, err
	}
	code, err := otp(ctx, mfa)
	if err != nil {
		return IdentityAuth{}, err
	}
	return VerifyMfa(ctx, httpc, userGraphQL, mfa.Challenge, code)
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
// (e.g. Authorization), and decodes the response "data" object into out. A status
// other than 200 is a *StatusError, and a non-empty GraphQL "errors" array is a
// *GraphQLError. It
// mirrors svcclient's envelope handling, except that Codes here is parallel to Messages
// (see GraphQLError).
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
		return &StatusError{URL: url, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code      string   `json:"code"`
				Challenge string   `json:"challenge"`
				ExpiresAt string   `json:"expiresAt"`
				Methods   []string `json:"methods"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("userclient: decode response: %w", err)
	}
	// The password was right and the account has a second factor: a typed answer carrying the
	// challenge, not a refusal to be read from a message. Only a lone, well-formed one counts.
	if len(envelope.Errors) == 1 && envelope.Errors[0].Extensions.Code == codeMfaChallenge &&
		envelope.Errors[0].Extensions.Challenge != "" {
		x := envelope.Errors[0].Extensions
		return &MfaRequiredError{Challenge: x.Challenge, ExpiresAt: parseExpiry(x.ExpiresAt), Methods: x.Methods}
	}
	if len(envelope.Errors) > 0 {
		gqlErr := &GraphQLError{
			URL:      url,
			Messages: make([]string, 0, len(envelope.Errors)),
			Codes:    make([]string, 0, len(envelope.Errors)),
		}
		for _, e := range envelope.Errors {
			gqlErr.Messages = append(gqlErr.Messages, e.Message)
			gqlErr.Codes = append(gqlErr.Codes, e.Extensions.Code)
		}
		return gqlErr
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("userclient: decode data: %w", err)
		}
	}
	return nil
}

// GraphQLError is graphqlPost's answer when the server returned a GraphQL response
// carrying a non-empty "errors" array: the server was reached and refused. Codes is
// PARALLEL to Messages — Codes[i] is error i's extensions.code, "" where it set none —
// so a caller can ask a question about EVERY error, not just about whether some error
// carried a code.
type GraphQLError struct {
	URL      string
	Messages []string
	Codes    []string
}

// Error is "userclient: <url>: <m1>; <m2>", the text this client has always produced.
func (e *GraphQLError) Error() string {
	return fmt.Sprintf("userclient: %s: %s", e.URL, strings.Join(e.Messages, "; "))
}

// StatusError is graphqlPost's answer when the server replied with a status other than
// 200: the request reached something, and it refused before any GraphQL ran. Its text is
// the one this client has always produced, so a reader of the message sees no change; a
// caller that must act on the STATUS asks for it here instead of parsing the text.
type StatusError struct {
	URL        string
	StatusCode int
	Body       string // the response body, trimmed
}

// Error is "userclient: <url> returned <code>: <body>".
func (e *StatusError) Error() string {
	return fmt.Sprintf("userclient: %s returned %d: %s", e.URL, e.StatusCode, e.Body)
}

// IsUnauthorized reports whether err's chain holds a *StatusError whose status is 401:
// the endpoint refused the bearer token itself. Nothing else counts — not a 403, not a
// GraphQL error whose message talks about authorization, not a transport failure.
func IsUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusUnauthorized
}

// AllHaveCode reports whether err's chain holds a *GraphQLError in which EVERY error
// carried extensions.code == code.
//
// 🔴 EVERY, NOT ANY. A caller uses this to decide that a refusal is benign — dcctl
// treats CONFLICT as "already exists, carry on" — and a response carrying one CONFLICT
// beside an uncoded real failure is not benign. Answering on "any" would let the one
// swallow the other.
func AllHaveCode(err error, code string) bool {
	var gqlErr *GraphQLError
	if code == "" || !errors.As(err, &gqlErr) || len(gqlErr.Codes) == 0 {
		return false
	}
	for _, c := range gqlErr.Codes {
		if c != code {
			return false
		}
	}
	return true
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

// sharedTransport is this package's connection pool, used by every client it builds
// itself and filled in on a caller-supplied client that has none. Leaving
// http.Client.Transport nil would dial through http.DefaultTransport, which resolves
// the environment's proxy settings — for a login exchange carrying a password and for
// every call carrying the tenant access token — and shares one small pool with the rest
// of the process. httptransport states both.
//
// A caller that must dial through a proxy, or that needs its own TLS or dialer
// settings, supplies its own *http.Client; that transport is kept as-is.
var sharedTransport = httptransport.New()

// defaultClient is the client used when a caller supplies none. It is a package
// singleton rather than a fresh client per call because graphqlPost resolves the client
// on EVERY request: a new one each time would be a new pool each time, so nothing would
// ever be reused and every request would open a connection.
var defaultClient = &http.Client{Timeout: requestTimeout, Transport: sharedTransport}

// defaultHTTP returns the client a call should run on: the package default when httpc
// is nil, otherwise httpc — with a copy taken to fill in anything it left unstated.
//
// Both fill-ins close the same kind of hole, where an unset field does not mean "no
// policy" but "inherit a policy nobody chose". A nil Transport means
// http.DefaultTransport, with the process-wide proxy and pool above. A zero Timeout
// means no deadline at all, which the sessions make worse: they renew tokens on a
// detached context, so a caller that passes a client with Timeout 0 gets a login with
// neither a deadline nor a cancellation path. The caller's own client is never mutated.
func defaultHTTP(httpc *http.Client) *http.Client {
	if httpc == nil {
		return defaultClient
	}
	if httpc.Transport != nil && httpc.Timeout != 0 {
		return httpc
	}
	c := *httpc
	if c.Transport == nil {
		c.Transport = sharedTransport
	}
	if c.Timeout == 0 {
		c.Timeout = requestTimeout
	}
	return &c
}
