// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// The single sign-on flow: this service as an OpenID Connect relying party.
//
//	beginSso      public, rate limited. Writes a state record (kind, connection, nonce, the
//	              upstream PKCE verifier, the return address, the browser's own code
//	              challenge) and returns the provider's authorization URL, built from the
//	              connection's SNAPSHOT -- discovery is never fetched live.
//	callback      GET /sso/callback/{connection}. Consumes the state exactly once, refuses
//	              it unless it belongs to the connection in the path, exchanges the code
//	              with PKCE, verifies the ID token, resolves the identity (explicit links
//	              only), and writes a 60-second single-use login code.
//	completeSso   exchanges the login code, with the verifier only the beginning browser
//	              holds, for an identity token -- the same shape password login returns.
//
// Three properties carry the security of it, and each has a test that fails without it:
// the state is single-use and bound to one connection (a replayed or cross-connection
// callback finds nothing), the login code is useless without the beginning browser's
// verifier (a code planted in another browser cannot be completed), and an identity is
// reached only through a link someone made on purpose.

// Kinds of state record and login code. Every redeemer checks its record's kind FIRST.
const (
	ssoKindLogin     = "login"
	ssoKindLink      = "link"
	ssoKindAuthorize = "authorize" // the MCP authorize page's sign-in; resumed only by ResumeAuthorizeSso
)

// Outcomes, as counted on devicechain_usermanagement_sso_logins_total and written to the
// audit journal. Every refusal has a name of its own.
const (
	SsoOutcomeSuccess                   = "success"
	SsoOutcomeStateInvalid              = "state_invalid"
	SsoOutcomeIdpError                  = "idp_error"
	SsoOutcomeRateLimited               = "rate_limited"
	SsoOutcomeStateStoreFull            = "state_store_full"
	SsoOutcomeRefusedTokenInvalid       = "refused_token_invalid"
	SsoOutcomeRefusedIssuerMismatch     = "refused_issuer_mismatch"
	SsoOutcomeRefusedUnverifiedEmail    = "refused_unverified_email"
	SsoOutcomeRefusedUnverifiedDomain   = "refused_unverified_domain"
	SsoOutcomeRefusedRequiredClaim      = "refused_required_claim"
	SsoOutcomeRefusedSystemRole         = "refused_system_role_identity"
	SsoOutcomeRefusedLinkRequired       = "refused_link_required"
	SsoOutcomeRefusedIdentityDisabled   = "refused_identity_disabled"
	SsoOutcomeRefusedLinkMismatch       = "refused_link_mismatch"
	SsoOutcomeRefusedAlreadyLinked      = "refused_already_linked"
	SsoOutcomeRefusedLinkSession        = "refused_link_session"
	SsoOutcomeRefusedConnectionUnusable = "refused_connection_unavailable"
	SsoOutcomeServerError               = "server_error"
)

var (
	// ErrSsoUnavailable is what a caller of the public sign-in calls gets when single
	// sign-on cannot be used for the request: no public issuer URL, no connection, the
	// connection disabled or its discovery not approved, or the state store unusable. One
	// text for all of them, so the call says nothing about which.
	ErrSsoUnavailable = errors.New("single sign-on is not available for this sign-in")
	// ErrSsoRateLimited refuses a begin over the per-source or global allowance.
	ErrSsoRateLimited = errors.New("too many sign-in attempts; try again shortly")
	// ErrSsoInvalidRequest refuses malformed input before anything is written.
	ErrSsoInvalidRequest = errors.New("invalid single sign-on request")
	// ErrSsoCodeInvalid is completeSso's single answer to a code that is unknown, used,
	// expired, of another kind, or not matched by the verifier.
	ErrSsoCodeInvalid = errors.New("invalid or expired sign-in code")
	// ErrSsoNoPublicIssuer is returned to the administrator creating a connection on an
	// instance that has no public issuer URL, where no sign-in could ever return.
	ErrSsoNoPublicIssuer = errors.New("sso requires a public issuer URL")
)

// SsoMetrics receives the flow's counters. The Manager works with none installed.
type SsoMetrics interface {
	// Login counts one outcome for a connection. connection is the connection's token, or
	// "unknown" when the request named none that exists -- never request-supplied text, so
	// the label set stays bounded by what a superuser created.
	Login(connection, outcome string)
	// IdpFetchFailure counts a request to the provider (token endpoint or key set) that
	// failed to complete.
	IdpFetchFailure(connection string)
}

// ssoState is the record beginSso writes and the callback consumes.
type ssoState struct {
	Kind         string `json:"kind"`
	ConnectionID uint   `json:"connection_id"`
	Nonce        string `json:"nonce"`
	Verifier     string `json:"verifier"` // the upstream PKCE verifier
	ReturnTo     string `json:"return_to"`
	// CodeChallenge is the BEGINNING BROWSER's challenge (login kind); the login code is
	// redeemable only with its verifier.
	CodeChallenge string    `json:"code_challenge,omitempty"`
	BeganAt       time.Time `json:"began_at"`
	// LinkSession and LinkEmail bind a link-kind state to the password sign-in that began it.
	LinkSession auth.SessionID `json:"link_sid,omitempty"`
	LinkEmail   string         `json:"link_email,omitempty"`
	// Authorize is the pending OAuth authorization request (authorize kind), held HERE so it
	// never travels through the provider or comes back from the browser.
	Authorize *AuthorizeParams `json:"authorize,omitempty"`
}

// ssoCode is the record the callback writes and completeSso consumes.
type ssoCode struct {
	Kind          string    `json:"kind"`
	ConnectionID  uint      `json:"connection_id"`
	Email         string    `json:"email"`
	CodeChallenge string    `json:"code_challenge"`
	Anchor        time.Time `json:"anchor"`
	// Authorize carries the pending request from an authorize-kind state to its resume.
	Authorize *AuthorizeParams `json:"authorize,omitempty"`
}

const (
	// SsoStateTTL is how long a begun sign-in stays completable.
	SsoStateTTL = 10 * time.Minute
	// SsoCodeTTL is how long a login code stays redeemable.
	SsoCodeTTL = 60 * time.Second

	maxSsoField = 2048
)

// A client's code challenge is base64url(SHA-256(verifier)): 32 bytes, 43 characters.
var ssoChallengeShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// A state or code key: base64url, bounded.
var ssoKeyShape = regexp.MustCompile(`^[A-Za-z0-9_-]{20,100}$`)

// returnPaths are the only places a completed sign-in may be sent back to, on the issuer's
// own origin: the console's and the dashboard's completion pages.
var returnPaths = map[string]struct{}{
	"/login/sso/complete": {},
	"/dash/sso/complete":  {},
}

// SetSsoStores installs the two KV buckets the flow uses. Single sign-on is available only
// when both are installed AND the instance has a public issuer URL.
func (m *Manager) SetSsoStores(state, codes nats.KeyValue) {
	m.ssoStateKV, m.ssoCodesKV = state, codes
}

// SetSsoMetrics installs the counters.
func (m *Manager) SetSsoMetrics(sm SsoMetrics) { m.ssoMetrics = sm }

// SsoReady reports whether single sign-on can run at all on this instance.
func (m *Manager) SsoReady() bool {
	return m.issuerUrl != "" && m.ssoStateKV != nil && m.ssoCodesKV != nil && m.ssoClient != nil
}

func (m *Manager) countSso(connection, outcome string) {
	if m.ssoMetrics != nil {
		m.ssoMetrics.Login(connection, outcome)
	}
}

func (m *Manager) countIdpFailure(connection string) {
	if m.ssoMetrics != nil {
		m.ssoMetrics.IdpFetchFailure(connection)
	}
}

// SsoCallbackURL is the redirect URI registered at the provider for a connection. Every
// connection has its own, which is what makes the state-versus-path check a defence.
func (m *Manager) SsoCallbackURL(connectionToken string) string {
	return strings.TrimSuffix(m.issuerUrl, "/") + "/sso/callback/" + connectionToken
}

// BeginSsoInput is a beginSso request. Exactly one of Email and Connection selects the
// connection: the email by the exact verified domain it is in, Connection by token.
type BeginSsoInput struct {
	Email         string
	Connection    string
	ReturnTo      string
	CodeChallenge string
}

// SsoBegin is beginSso's answer.
type SsoBegin struct {
	AuthorizationURL string
}

// BeginSso starts a sign-in. source is the caller's address, for rate limiting. It returns
// (nil, nil) when Email names no connection (the person signs in some other way) and
// ErrSsoUnavailable when a connection is found but cannot be used.
func (m *Manager) BeginSso(ctx context.Context, in BeginSsoInput, source string) (*SsoBegin, error) {
	if !m.SsoReady() {
		return nil, ErrSsoUnavailable
	}
	if !m.ssoLimiter.allow(source) {
		m.countSso("unknown", SsoOutcomeRateLimited)
		return nil, ErrSsoRateLimited
	}
	if len(in.Email) > 256 || len(in.Connection) > 128 || len(in.ReturnTo) > maxSsoField ||
		!ssoChallengeShape.MatchString(in.CodeChallenge) || (in.Email == "") == (in.Connection == "") {
		return nil, ErrSsoInvalidRequest
	}
	returnTo, err := m.checkReturnTo(in.ReturnTo)
	if err != nil {
		return nil, ErrSsoInvalidRequest
	}

	var conn *iam.IdpConnection
	if in.Connection != "" {
		conn, err = m.iam.IdpConnectionByToken(ctx, in.Connection)
	} else {
		var domain string
		domain, err = emailDomain(in.Email)
		if err != nil {
			return nil, ErrSsoInvalidRequest
		}
		conn, err = m.iam.IdpConnectionByDomain(ctx, domain)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSsoUnavailable
		}
		log.Error().Err(err).Msg("beginSso could not resolve its connection.")
		return nil, ErrSsoUnavailable
	}
	return m.beginWithState(ctx, conn, ssoState{
		Kind: ssoKindLogin, ReturnTo: returnTo, CodeChallenge: in.CodeChallenge,
	})
}

// emailDomain returns the normalised domain of an address, or an error.
func emailDomain(email string) (string, error) {
	email = iam.NormalizeEmail(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 || strings.Count(email, "@") != 1 {
		return "", ErrSsoInvalidRequest
	}
	return NormalizeDomain(email[at+1:])
}

// usableConnection is whether a connection can begin a sign-in at all: enabled, with a
// snapshot a person has approved.
func usableConnection(conn *iam.IdpConnection) bool {
	return conn.Enabled && conn.DiscoverySnapshot != nil && conn.DiscoveryApprovedAt != nil
}

// beginWithState writes st (completed with the connection, nonce, verifier and time) and
// returns the provider's URL.
func (m *Manager) beginWithState(ctx context.Context, conn *iam.IdpConnection, st ssoState) (*SsoBegin, error) {
	if !usableConnection(conn) {
		m.countSso(conn.Token, SsoOutcomeRefusedConnectionUnusable)
		return nil, ErrSsoUnavailable
	}
	key, err := randomKey()
	if err != nil {
		return nil, ErrSsoUnavailable
	}
	nonce, err := randomKey()
	if err != nil {
		return nil, ErrSsoUnavailable
	}
	st.ConnectionID, st.Nonce, st.Verifier, st.BeganAt = conn.ID, nonce, oauth2.GenerateVerifier(), time.Now().UTC()
	b, err := json.Marshal(st)
	if err != nil {
		return nil, ErrSsoUnavailable
	}
	if _, err := m.ssoStateKV.Create(key, b); err != nil {
		if jetStreamStoreFull(err) {
			// FAIL CLOSED, and say so on the counter the alert watches: a flood of begins
			// has filled the bucket, and single sign-on stays down until it drains. Password
			// sign-in does not use this bucket and is unaffected.
			m.countSso(conn.Token, SsoOutcomeStateStoreFull)
			log.Error().Msg("The single sign-on state store is full; beginSso is refusing.")
		} else {
			log.Error().Err(err).Msg("beginSso could not write its state record.")
		}
		return nil, ErrSsoUnavailable
	}

	cfg := m.oauthConfig(conn, "")
	url := cfg.AuthCodeURL(key, oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(nonce))
	return &SsoBegin{AuthorizationURL: url}, nil
}

// oauthConfig is the OAuth client for a connection, built from its SNAPSHOT.
func (m *Manager) oauthConfig(conn *iam.IdpConnection, secret string) *oauth2.Config {
	scopes := conn.Scopes
	if !containsString(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &oauth2.Config{
		ClientID:     conn.ClientID,
		ClientSecret: secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   conn.DiscoverySnapshot.AuthorizationEndpoint,
			TokenURL:  conn.DiscoverySnapshot.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleAutoDetect,
		},
		RedirectURL: m.SsoCallbackURL(conn.Token),
		Scopes:      scopes,
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// randomKey returns 256 random bits as a URL-safe string: the state value, the nonce,
// and the login code.
func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// checkReturnTo returns the canonical form of a return address, or an error. The address
// must be absolute, on the issuer's own origin (scheme and host), and exactly one of the
// fixed completion paths: no query, no fragment, no userinfo, no backslash, no encoded dot,
// no "//host". It is compared after parsing, and the path is compared in its ESCAPED form so
// "%2e%2e" and its relatives are not equal to anything on the list.
func (m *Manager) checkReturnTo(raw string) (string, error) {
	if raw == "" || len(raw) > maxSsoField || strings.ContainsAny(raw, "\\\r\n\t ") || strings.Contains(raw, "..") {
		return "", ErrSsoInvalidRequest
	}
	issuer, err := url.Parse(m.issuerUrl)
	if err != nil || issuer.Host == "" {
		return "", ErrSsoInvalidRequest
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ErrSsoInvalidRequest
	}
	if u.Scheme != issuer.Scheme || u.Host != issuer.Host || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", ErrSsoInvalidRequest
	}
	if _, ok := returnPaths[u.EscapedPath()]; !ok || u.Path != u.EscapedPath() {
		return "", ErrSsoInvalidRequest
	}
	return u.String(), nil
}

// consumeKV reads a record and deletes it with a revision check, so exactly one caller of N
// holds it. (nil, false, nil) is "no such record, or somebody else just took it".
func consumeKV(kv nats.KeyValue, key string) ([]byte, bool, error) {
	if !ssoKeyShape.MatchString(key) {
		return nil, false, nil
	}
	entry, err := kv.Get(key)
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := kv.Delete(key, nats.LastRevision(entry.Revision())); err != nil {
		if errors.Is(err, nats.ErrKeyRevisionMismatch) || errors.Is(err, nats.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return entry.Value(), true, nil
}

// jetStreamStoreFull reports whether err is JetStream refusing a write because the bucket
// is at its byte ceiling: a generic "store failed" API error whose description is the
// store's own "maximum bytes exceeded". Mirrors the credential store's check.
func jetStreamStoreFull(err error) bool {
	var apiErr *nats.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == 10077 && apiErr.Description == "maximum bytes exceeded"
}

// SsoCallbackResult is what the HTTP handler does with a callback. Location is where to
// redirect; empty means there is no trustworthy place to send the browser (the state was
// unusable), and the handler answers with an error page instead.
type SsoCallbackResult struct {
	Location string
	Outcome  string
}

// HandleSsoCallback processes the provider's redirect for the connection named in the path.
// query is the callback's query string; it is untrusted in its entirety.
func (m *Manager) HandleSsoCallback(ctx context.Context, connectionToken string, query url.Values) SsoCallbackResult {
	if !m.SsoReady() {
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	// The state is consumed FIRST, before anything about the connection is looked at: a
	// replayed callback, a guessed one, or one for the wrong connection finds nothing, and
	// nothing it carried is acted on.
	raw, ok, err := consumeKV(m.ssoStateKV, query.Get("state"))
	if err != nil {
		log.Error().Err(err).Msg("The single sign-on state store could not be read.")
		m.countSso("unknown", SsoOutcomeServerError)
		return SsoCallbackResult{Outcome: SsoOutcomeServerError}
	}
	if !ok {
		m.countSso("unknown", SsoOutcomeStateInvalid)
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	var st ssoState
	if err := json.Unmarshal(raw, &st); err != nil || time.Since(st.BeganAt) > SsoStateTTL {
		m.countSso("unknown", SsoOutcomeStateInvalid)
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	conn, err := m.iam.IdpConnectionByToken(ctx, connectionToken)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Error().Err(err).Msg("The single sign-on callback could not load its connection.")
		}
		m.countSso("unknown", SsoOutcomeStateInvalid)
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	// THE MIX-UP DEFENCE. Every connection has its own callback path, so the connection a
	// state record was created for must be the connection the provider's redirect arrived
	// at. A state begun for X and delivered at Y's path is refused even though it is valid.
	if st.ConnectionID != conn.ID {
		m.countSso(conn.Token, SsoOutcomeStateInvalid)
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	// An authorize-kind state returns to the authorize endpoint itself, an address the server
	// computes; it is never read from the record, so no state can send a browser elsewhere.
	var returnTo string
	if st.Kind == ssoKindAuthorize {
		returnTo, err = m.authorizeReturnURL()
	} else {
		returnTo, err = m.checkReturnTo(st.ReturnTo)
	}
	if err != nil {
		m.countSso(conn.Token, SsoOutcomeStateInvalid)
		return SsoCallbackResult{Outcome: SsoOutcomeStateInvalid}
	}
	finish := func(outcome string, email string, params url.Values) SsoCallbackResult {
		return m.ssoRefusal(ctx, conn, outcome, email, returnTo, params)
	}
	if !usableConnection(conn) {
		return finish(SsoOutcomeRefusedConnectionUnusable, "", nil)
	}
	snap := conn.DiscoverySnapshot

	// RFC 9207: when the provider says which issuer answered, it must be the connection's.
	if iss := query.Get("iss"); iss != "" && iss != snap.Issuer {
		return finish(SsoOutcomeRefusedIssuerMismatch, "", nil)
	}
	if query.Get("error") != "" || query.Get("code") == "" {
		return finish(SsoOutcomeIdpError, "", nil)
	}

	secret, err := m.connectionSecret(ctx, conn)
	if err != nil {
		log.Error().Err(err).Str("connection", conn.Token).Msg("The connection's client secret could not be read.")
		return finish(SsoOutcomeServerError, "", nil)
	}
	cctx, err := m.ssoContext(ctx)
	if err != nil {
		return finish(SsoOutcomeServerError, "", nil)
	}
	cfg := m.oauthConfig(conn, secret)
	tok, err := cfg.Exchange(cctx, query.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		m.countIdpFailure(conn.Token)
		log.Warn().Err(err).Str("connection", conn.Token).Msg("The identity provider's token endpoint refused or failed the code exchange.")
		return finish(SsoOutcomeIdpError, "", nil)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return finish(SsoOutcomeRefusedTokenInvalid, "", nil)
	}

	claims, err := m.VerifySsoIDToken(ctx, conn, rawID, st.Nonce)
	if err != nil {
		if isFetchFailure(err) {
			m.countIdpFailure(conn.Token)
			log.Warn().Err(err).Str("connection", conn.Token).Msg("The identity provider's keys could not be fetched.")
			return finish(SsoOutcomeIdpError, "", nil)
		}
		log.Warn().Err(err).Str("connection", conn.Token).Msg("An ID token was refused.")
		return finish(SsoOutcomeRefusedTokenInvalid, "", nil)
	}
	if len(claims.Subject) > 512 {
		return finish(SsoOutcomeRefusedTokenInvalid, "", nil)
	}
	if err := bindEmail(conn, claims); err != nil || len(claims.Email) > 256 {
		return finish(SsoOutcomeRefusedTokenInvalid, "", nil)
	}
	if err := checkSsoClaims(conn, claims); err != nil {
		log.Warn().Err(err).Str("connection", conn.Token).Msg("An ID token did not meet the connection's requirements.")
		return finish(SsoOutcomeRefusedRequiredClaim, claims.Email, nil)
	}

	switch st.Kind {
	case ssoKindLogin, ssoKindAuthorize:
		// Same identity resolution; they differ in the record written (its kind, and for
		// authorize the pending request) and in who may redeem it.
		return m.finishLogin(ctx, conn, st, claims, returnTo, finish)
	case ssoKindLink:
		return m.finishLink(ctx, conn, st, claims, returnTo, finish)
	}
	// An unknown-kind state is not completable.
	return finish(SsoOutcomeStateInvalid, "", nil)
}

// isFetchFailure reports whether a verification error was a failure to REACH the provider's
// key set rather than a judgement of the token.
func isFetchFailure(err error) bool {
	var f *idpFetchError
	return errors.As(err, &f)
}

// connectionSecret reads a connection's client secret. A connection with none is a public
// client (the code exchange then relies on PKCE alone).
func (m *Manager) connectionSecret(ctx context.Context, conn *iam.IdpConnection) (string, error) {
	if m.secrets == nil {
		return "", errors.New("no secret store is configured")
	}
	b, err := m.secrets.Resolve(ctx, iam.IdpConnectionSecretRef(conn.ID))
	if err != nil {
		if errors.Is(err, secrets.ErrSecretNotFound) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// ssoRefusal counts and audits an outcome and builds the redirect back to the return
// address carrying only a coarse error class: the person is told what to do next, never
// which check failed.
func (m *Manager) ssoRefusal(ctx context.Context, conn *iam.IdpConnection, outcome, email, returnTo string, params url.Values) SsoCallbackResult {
	m.countSso(conn.Token, outcome)
	if strings.HasPrefix(outcome, "refused_") {
		// The actor is the address the provider asserted, which is exactly why it is worth
		// recording; nothing else about the token is.
		m.recordAuth(ctx, rdb.AuditOpSsoRefused, email, "")
	}
	class := "refused"
	switch outcome {
	case SsoOutcomeRefusedLinkRequired:
		class = "link_required"
	case SsoOutcomeIdpError, SsoOutcomeServerError:
		class = "idp_error"
	case SsoOutcomeRefusedGroupsOverage:
		class = "groups_overage"
	}
	q := url.Values{"error": {class}}
	for k, v := range params {
		q[k] = v
	}
	return SsoCallbackResult{Location: withQuery(returnTo, q), Outcome: outcome}
}

func withQuery(base string, q url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// finishLogin runs rule C for a login-kind state and, on success, writes the login code.
func (m *Manager) finishLogin(ctx context.Context, conn *iam.IdpConnection, st ssoState, claims *ssoClaims,
	returnTo string, finish func(string, string, url.Values) SsoCallbackResult) SsoCallbackResult {

	// Rule E reads the person's groups BEFORE an identity is resolved, so a token whose group
	// list cannot be trusted refuses the sign-in without provisioning anyone.
	var (
		groups   map[string]struct{}
		mappings []iam.IdpRoleMappingView
	)
	if conn.Kind == iam.IdpKindTenant {
		var err error
		if mappings, err = m.iam.ListIdpRoleMappings(ctx, conn.ID); err != nil {
			log.Error().Err(err).Str("connection", conn.Token).Msg("Single sign-on could not read the connection's role mappings.")
			return finish(SsoOutcomeServerError, claims.Email, nil)
		}
		var refused string
		if groups, refused = ssoGroups(conn, claims, len(mappings) > 0); refused != "" {
			log.Warn().Str("connection", conn.Token).Str("outcome", refused).Msg("A sign-in's groups could not be used.")
			return finish(refused, claims.Email, nil)
		}
	}
	id, outcome, err := m.resolveSsoIdentity(ctx, conn, claims)
	if err != nil {
		log.Error().Err(err).Str("connection", conn.Token).Msg("Single sign-on could not resolve an identity.")
		return finish(SsoOutcomeServerError, claims.Email, nil)
	}
	if outcome != "" {
		return finish(outcome, claims.Email, nil)
	}
	// Rule E: the managed memberships follow the provider's groups at every sign-in.
	if conn.Kind == iam.IdpKindTenant {
		if err := m.syncSsoMemberships(ctx, conn, id, groups, mappings); err != nil {
			log.Error().Err(err).Str("connection", conn.Token).Msg("Single sign-on could not update the person's memberships.")
			return finish(SsoOutcomeServerError, claims.Email, nil)
		}
	}
	code, err := randomKey()
	if err != nil {
		return finish(SsoOutcomeServerError, claims.Email, nil)
	}
	rec, _ := json.Marshal(ssoCode{
		Kind: st.Kind, ConnectionID: conn.ID, Email: id.Email,
		CodeChallenge: st.CodeChallenge, Anchor: time.Now().UTC(), Authorize: st.Authorize,
	})
	if _, err := m.ssoCodesKV.Create(code, rec); err != nil {
		log.Error().Err(err).Msg("Single sign-on could not write its login code.")
		return finish(SsoOutcomeServerError, claims.Email, nil)
	}
	param := "code"
	if st.Kind == ssoKindAuthorize {
		param = authorizeResumeParam
	}
	return SsoCallbackResult{Location: withQuery(returnTo, url.Values{param: {code}}), Outcome: SsoOutcomeSuccess}
}

// CompleteSso exchanges a login code and the beginning browser's PKCE verifier for the same
// result password login returns. Every failure is ErrSsoCodeInvalid.
//
// The session starts HERE, anchored at the callback's time recorded in the code, so a code
// that is never redeemed leaves no session behind.
func (m *Manager) CompleteSso(ctx context.Context, code, codeVerifier string) (*IdentityAuth, error) {
	if !m.SsoReady() {
		return nil, ErrSsoUnavailable
	}
	if len(code) > 128 || len(codeVerifier) < 43 || len(codeVerifier) > 128 {
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
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Kind != ssoKindLogin {
		return nil, ErrSsoCodeInvalid
	}
	// The code was written by a callback and handed to whichever browser the provider sent
	// back. Only the browser that BEGAN the flow holds the verifier for the challenge the
	// state recorded, so a code planted in another browser cannot be completed.
	if !verifyPKCE(codeVerifier, rec.CodeChallenge) {
		return nil, ErrSsoCodeInvalid
	}
	return m.completeSsoRecord(ctx, rec)
}

// completeSsoRecord starts the session for a login code that has already been consumed, kind
// checked and matched to its browser's secret. It is shared by CompleteSso and the authorize
// page's resume, which differ only in who may redeem a record.
func (m *Manager) completeSsoRecord(ctx context.Context, rec ssoCode) (*IdentityAuth, error) {
	if time.Since(rec.Anchor) > SsoCodeTTL+10*time.Second {
		return nil, ErrSsoCodeInvalid
	}

	conn, err := m.iam.IdpConnectionByID(ctx, rec.ConnectionID)
	// The connection is judged again as it is NOW, as the callback judged it: a connection
	// disabled, or whose discovery approval was withdrawn, in the minute since the callback
	// starts no session. This matters most for an INSTANCE connection, whose identity token
	// carries system authorities straight to the admin plane without passing admitTenant.
	if err != nil || !usableConnection(conn) {
		return nil, ErrSsoCodeInvalid
	}
	id, err := m.iam.IdentityByEmail(ctx, rec.Email)
	if err != nil || !id.Enabled {
		return nil, ErrSsoCodeInvalid
	}
	// And the link the callback resolved must still exist: an identity unlinked since then
	// is not signed in by a code minted before the unlink.
	if linked, err := m.iam.IdentityLinkedTo(ctx, id.ID, conn.ID); err != nil || !linked {
		return nil, ErrSsoCodeInvalid
	}
	// Rule C.1 again: the identity may have been granted a system role since the callback.
	if conn.Kind == iam.IdpKindTenant && len(id.SystemRoles) > 0 {
		m.countSso(conn.Token, SsoOutcomeRefusedSystemRole)
		return nil, ErrSsoCodeInvalid
	}
	sid, err := m.startSsoSession(id.Email, conn, rec.Anchor)
	if err != nil {
		return nil, err
	}
	tok, err := m.issueFederatedIdentity(id, conn, sid)
	if err != nil {
		if errors.Is(err, auth.ErrNoSessionEpoch) {
			return nil, ErrSsoCodeInvalid
		}
		return nil, err
	}
	m.countSso(conn.Token, SsoOutcomeSuccess)
	m.recordAuth(ctx, rdb.AuditOpSsoLogin, id.Email, "")
	memberships, err := m.sessionMemberships(ctx, sid, id)
	if err != nil {
		return nil, err
	}
	return &IdentityAuth{
		IdentityToken: tok.Token,
		ExpiresAt:     tok.ExpiresAt,
		Superuser:     isSuperuser(id),
		Memberships:   memberships,
	}, nil
}

// issueFederatedIdentity mints the identity token for a single sign-on.
//
// A TENANT connection mints through a function that takes NO roles and NO authorities --
// they are not parameters, so there is no value that could be passed wrongly -- and the
// token it produces carries none: nothing a tenant's identity provider vouches for can
// reach the admin plane, whatever identity it resolved to. An INSTANCE connection mints
// with the identity's system authorities, which is its whole purpose and is reachable only
// by an identity a superuser linked on purpose.
func (m *Manager) issueFederatedIdentity(id *iam.Identity, conn *iam.IdpConnection, sid auth.SessionID) (auth.IssuedToken, error) {
	switch conn.Kind {
	case iam.IdpKindTenant:
		return m.issueTenantFederatedIdentity(id.Email, auth.SessionEpoch(id.SessionEpoch), sid)
	case iam.IdpKindInstance:
		m.mu.RLock()
		issuer := m.issuer
		m.mu.RUnlock()
		return issuer.IssueIdentity(id.Email, auth.SessionEpoch(id.SessionEpoch), sid,
			roleTokens(id.SystemRoles), id.SystemAuthorities(), uuid.NewString())
	}
	return auth.IssuedToken{}, fmt.Errorf("unknown connection kind %q", conn.Kind)
}

// issueTenantFederatedIdentity is the structural half of the guarantee above.
func (m *Manager) issueTenantFederatedIdentity(email string, epoch auth.SessionEpoch, sid auth.SessionID) (auth.IssuedToken, error) {
	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()
	return issuer.IssueIdentity(email, epoch, sid, nil, nil, uuid.NewString())
}

// claimsDigest is a short fingerprint of what a refused sign-in asserted, kept on a link
// request so a superuser can tell two attempts apart without the claims being stored.
func claimsDigest(c *ssoClaims) string {
	sum := sha256.Sum256([]byte(c.Subject + "\x00" + c.Email))
	return hex.EncodeToString(sum[:])
}
