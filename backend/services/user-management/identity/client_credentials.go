// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// The OAuth 2.1 client_credentials grant (RFC 6749 §4.4), for a tenant's SERVICE ACCOUNTS:
// an integration presents its client id and secret and receives an ordinary short-lived
// tenant access token, with no person's refresh token anywhere in the chain.
//
// 🔑 THE GRANT TYPE DECIDES WHICH PRINCIPAL AUTHENTICATES. A client_credentials request is
// authenticated against iam_service_accounts and nothing else, and every other grant against
// iam_oauth_clients and nothing else — so an operator's OAuth client can never obtain a token
// on this grant, an account can never be used to redeem a code, and no client-id prefix or
// reserved-name list has to keep the two apart.
//
// What the token carries is decided here, from the account as stored now: its tenant (a
// request cannot name another), its roles' authorities (no viewer baseline), its scope as
// "scp", and — when a resource indicator was sent — the RFC 8707 audience. There is no
// refresh token: the account mints again, and every mint re-reads the account, so a disable,
// a role change or the end of a secret's overlap reaches the next token. One already issued
// lives the scoped access lifetime, five minutes.

// errInvalidTarget is RFC 8707 §2's error for a resource indicator the AS refuses.
func errInvalidTarget(desc string) *oauthError {
	return &oauthError{Code: "invalid_target", Desc: desc, Status: http.StatusBadRequest}
}

// errClientAuthFailed is the ONE answer for every authentication failure of this grant —
// unknown client, wrong secret, expired secret, disabled account — so a caller cannot probe
// which of them it hit.
func errClientAuthFailed() *oauthError { return errInvalidClient("client authentication failed") }

// The token endpoint's client_credentials limits, in-process per replica (the beginSso
// shape, sso_limiter.go):
//
//   - every ATTEMPT charges its source BEFORE the secret is compared, which bounds what one
//     sender can make the endpoint do — the compare is unthrottled per principal
//     (CredentialPolicies says why). An IPv6 source is keyed by its /64, the block one
//     subscriber is routinely given, so walking addresses inside it buys nothing;
//   - an attempt from a source that has not minted recently also charges a GLOBAL bucket,
//     which bounds a sender who varies the source. A source that HAS minted recently (a
//     running integration) is exempt from it: the global bucket is shared by every tenant,
//     so charging it for everyone would let a stranger who sprays from enough addresses
//     hold every tenant's integrations at the limit without knowing a single client id;
//   - every AUTHENTICATED mint charges the account's own bucket, which bounds a runaway
//     integration without letting a stranger who knows its client id starve it: failed
//     attempts never touch that bucket.
var (
	serviceAccountAttemptLimits = limiterLimits{
		perKeyBurst: 20, perKeyEvery: 3 * time.Second, maxKeys: 10000,
	}
	// serviceAccountGlobalLimits is one bucket (a single key) for attempts from sources
	// with no recent mint.
	serviceAccountGlobalLimits = limiterLimits{
		perKeyBurst: 200, perKeyEvery: 100 * time.Millisecond, maxKeys: 1,
	}
	serviceAccountMintLimits = limiterLimits{
		perKeyBurst: 10, perKeyEvery: 30 * time.Second, maxKeys: 10000,
	}
)

// serviceAccountTrustedSourceTTL is how long a source that minted stays exempt from the
// global bucket. Longer than a token's lifetime, so an integration that mints once per
// token stays exempt for as long as it keeps running.
const serviceAccountTrustedSourceTTL = time.Hour

// attemptSourceKey is the attempt limiter's key for a source address: an IPv4 address as
// it is, an IPv6 address by its /64, anything unparseable verbatim.
func attemptSourceKey(source string) string {
	ip := net.ParseIP(source)
	if ip == nil {
		return source
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// trustedSources remembers which sources minted recently. The zero value is ready to use.
type trustedSources struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// trustedSourcesMax bounds the map; past it, entries that have aged out are dropped, and if
// that frees nothing the map is cleared — forgetting only ever puts a source back under the
// global bucket.
const trustedSourcesMax = 10000

func (t *trustedSources) has(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.seen[key]
	return ok && now.Sub(at) < serviceAccountTrustedSourceTTL
}

func (t *trustedSources) mark(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]time.Time{}
	}
	if _, ok := t.seen[key]; !ok && len(t.seen) >= trustedSourcesMax {
		for k, at := range t.seen {
			if now.Sub(at) >= serviceAccountTrustedSourceTTL {
				delete(t.seen, k)
			}
		}
		if len(t.seen) >= trustedSourcesMax {
			t.seen = map[string]time.Time{}
		}
	}
	t.seen[key] = now
}

// errSlowDown is the rate-limit refusal: HTTP 429 with a Retry-After of at least one
// second (the header counts whole seconds, so a sub-second refill rounds up rather than
// down to no header at all).
func errSlowDown(retryAfter time.Duration) *oauthError {
	secs := int((retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return &oauthError{Code: "slow_down", Desc: "too many token requests; retry later",
		Status: http.StatusTooManyRequests, RetryAfter: secs}
}

// maxResourceLen bounds a resource indicator.
const maxResourceLen = 2048

// ClientCredentialsRequest is a parsed client_credentials token request.
type ClientCredentialsRequest struct {
	ClientID  string
	Secret    string
	Presented bool
	Scope     string
	Resources []string
	// Source is the request's source address, the attempt limiter's key.
	Source string
}

// ValidateResource checks an RFC 8707 resource indicator: an absolute URI with no fragment,
// https — or http for a loopback host only, the carve-out redirect URIs have.
func ValidateResource(resource string) error {
	if len(resource) > maxResourceLen {
		return errInvalidTarget("resource is too long")
	}
	u, err := url.Parse(resource)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" || strings.Contains(resource, "#") {
		return errInvalidTarget("resource must be an absolute URI without a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return errInvalidTarget("resource must use https")
		}
	default:
		return errInvalidTarget("resource must use https")
	}
	return nil
}

// ClientCredentials implements the client_credentials grant for a service account. Every
// authentication failure is the same invalid_client after the same work — one keyed
// compare, against the account's live secret whose hint the presented secret matches, or
// against the kind's dummy — so an unknown client, a disabled account, an expired secret
// and a wrong secret cannot be told apart by answer or by cost.
func (m *Manager) ClientCredentials(ctx context.Context, req ClientCredentialsRequest) (*OAuthTokens, error) {
	source := attemptSourceKey(req.Source)
	if !m.saAttempts.allow(source) {
		return nil, errSlowDown(serviceAccountAttemptLimits.perKeyEvery)
	}
	if !m.saTrusted.has(source, m.clock()) && !m.saGlobal.allow("") {
		return nil, errSlowDown(serviceAccountGlobalLimits.perKeyEvery)
	}
	if req.ClientID == "" || !req.Presented {
		return nil, errInvalidClient("client authentication required")
	}
	if len(req.Resources) > 1 {
		return nil, errInvalidTarget("at most one resource may be requested")
	}
	var audience []string
	if len(req.Resources) == 1 {
		if err := ValidateResource(req.Resources[0]); err != nil {
			return nil, err
		}
		audience = []string{req.Resources[0]}
	}
	var allow []string
	if scope := strings.TrimSpace(req.Scope); scope != "" {
		var err error
		if allow, err = scopeAllowance(scope); err != nil {
			return nil, errInvalidScope(err.Error())
		}
	}

	acct, err := m.iam.ServiceAccountByClientId(ctx, req.ClientID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Error().Err(err).Msg("Could not load a service account at the token endpoint.")
		return nil, errServer(errGrantUnavailable)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		acct = nil
	}
	now := m.clock()
	secret, err := m.checkServiceAccountSecret(ctx, acct, req.ClientID, req.Secret, now)
	if err != nil {
		return nil, err
	}
	if !m.saMints.allow(acct.ClientId) {
		return nil, errSlowDown(serviceAccountMintLimits.perKeyEvery)
	}

	t, err := m.iam.TenantByToken(ctx, acct.TenantId)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errInvalidGrant("tenant access denied")
	case err != nil:
		log.Error().Err(err).Msg("Could not load a service account's tenant at the token endpoint.")
		return nil, errServer(errGrantUnavailable)
	case t.PurgeState.Deleted() || !t.Enabled:
		return nil, errInvalidGrant("tenant access denied")
	}

	scope := auth.Scope{}
	if acct.IsScoped() {
		if scope, err = auth.NewScope(acct.Scope); err != nil {
			log.Error().Err(err).Str("tenant", acct.TenantId).Uint("serviceAccount", acct.ID).
				Msg("A service account's stored scope does not parse; it is refused rather than minted unscoped.")
			return nil, errInvalidGrant("tenant access denied")
		}
	}
	authorities := acct.Authorities()
	if allow != nil {
		authorities = auth.IntersectAuthorities(authorities, allow)
	}

	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()
	subject := auth.ServiceAccountSubject(acct.Token)
	access, err := issuer.IssueServiceAccountAccess(acct.TenantId, subject, roleTokens(acct.Roles), authorities,
		strings.TrimSpace(req.Scope), audience, scope, acct.ClientId, uuid.NewString())
	if err != nil {
		return nil, errServer(err.Error())
	}
	if err := m.iam.TouchServiceAccountSecret(ctx, secret.ID, now); err != nil {
		log.Warn().Err(err).Msg("Could not record a service account secret's last use.")
	}
	m.saTrusted.mark(source, now)
	m.recordAuth(ctx, rdb.AuditOpServiceAccountToken, subject, acct.TenantId)
	return &OAuthTokens{
		AccessToken: access.Token,
		Scope:       strings.TrimSpace(req.Scope),
		ExpiresIn:   expiresInSeconds(access.ExpiresAt, time.Now()),
	}, nil
}

// checkServiceAccountSecret authenticates a presented secret for acct (nil when the client
// id names no account), returning the secret row that matched.
//
// The candidate is the live secret whose hint — its last four characters, which the console
// shows — the presented secret ends with: at most two secrets are ever live, and the hint
// picks the one to compare without comparing twice. A disabled account, an unknown one, an
// expired secret or a hint that matches nothing compares against the dummy instead, which
// costs the same and never matches.
func (m *Manager) checkServiceAccountSecret(ctx context.Context, acct *iam.ServiceAccount, clientID, presented string, now time.Time) (*iam.ServiceAccountSecret, error) {
	if m.credentials == nil {
		return nil, errNoCredentialChecker
	}
	var candidates []*iam.ServiceAccountSecret
	tenant := ""
	if acct != nil && acct.Enabled {
		tenant = acct.TenantId
		for i := range acct.Secrets {
			s := &acct.Secrets[i]
			if s.Live(now) && s.Hint != "" && strings.HasSuffix(presented, s.Hint) {
				candidates = append(candidates, s)
			}
		}
	}
	principal := credential.Principal{Kind: credential.KindServiceAccount, ID: clientID, Tenant: tenant}
	if len(candidates) == 0 {
		err := m.credentials.Check(ctx, principal, presented, func(context.Context) (string, error) { return "", nil })
		return nil, serviceAccountCheckResult(err)
	}
	// Two live secrets sharing a hint is a one-in-sixteen-million coincidence; each is tried
	// in turn rather than leaving the older one unusable for its overlap.
	for _, c := range candidates {
		err := m.credentials.Check(ctx, principal, presented, func(context.Context) (string, error) { return c.Digest, nil })
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, credential.ErrMismatch) {
			return nil, serviceAccountCheckResult(err)
		}
	}
	return nil, errClientAuthFailed()
}

// serviceAccountCheckResult maps a failed check: a mismatch is the one invalid_client; any
// other error (the request budget, an unavailable store should the policy ever change) is a
// server_error, loud rather than mistaken for a wrong secret.
func serviceAccountCheckResult(err error) error {
	if err == nil || errors.Is(err, credential.ErrMismatch) {
		return errClientAuthFailed()
	}
	log.Warn().Err(err).Msg("A service account secret could not be checked.")
	return errServer(errGrantUnavailable)
}

// TokenEndpoint is the absolute URL of this instance's OAuth token endpoint, where a service
// account obtains its tokens, or "" when the instance has no OAuth issuer and so serves none.
func (m *Manager) TokenEndpoint() string {
	if m.issuerUrl == "" {
		return ""
	}
	return m.issuerUrl + TokenPath
}
