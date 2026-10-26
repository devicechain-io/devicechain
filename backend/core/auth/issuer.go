// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Default token lifetimes. Access tokens are short-lived; refresh tokens are
// long-lived and stored server-side (NATS KV) so they can be revoked.
const (
	DefaultAccessTTL  = 15 * time.Minute
	DefaultRefreshTTL = 7 * 24 * time.Hour
	// ServiceTokenTTL is the lifetime of a machine service token (ADR-044
	// amendment). Deliberately a fixed constant, decoupled from the operator-tunable
	// human access-token TTL: a service token's lifetime is an internal concern of
	// the sync-call primitive, and it must stay comfortably longer than svcclient's
	// refresh skew so a caller isn't forced to mint on every call.
	ServiceTokenTTL = 15 * time.Minute
	// DefaultScopedAccessTTL is the lifetime of a SCOPED tenant access token (ADR-081
	// decision 8). Narrowing a scoped member's access — narrowing the scope, removing a
	// role, disabling the membership — takes effect at their next refresh, which re-reads
	// the membership and mints the narrower grant (or refuses a disabled one), but cannot
	// refuse a request on an access token already issued: the data-plane validator does
	// not re-read the membership. The access lifetime IS the revocation window, so it is
	// shorter for the members whose access is the one most likely to be narrowed. It never
	// exceeds the issuer's unscoped access TTL (see NewIssuer).
	//
	// Five minutes is the value, not a placeholder. It is a third of the unscoped default,
	// so the window in which a narrowed customer user keeps their old reach is bounded at
	// five minutes, and it costs one refresh per five minutes per signed-in scoped user —
	// a refresh the console already performs on its own. It is not configurable: the
	// window is a promise the user guide states to the tenant admin doing the narrowing,
	// and a promise one operator can quietly lengthen is not one the guide can make.
	DefaultScopedAccessTTL = 5 * time.Minute
)

// Issuer mints RS256-signed access and refresh tokens. Only user-management
// holds an Issuer (it owns the private key); every other service holds only a
// Validator.
type Issuer struct {
	privateKey *rsa.PrivateKey
	kid        string
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
	// scopedAccessTTL is the lifetime of a scoped access token: never longer than
	// accessTTL. See DefaultScopedAccessTTL.
	scopedAccessTTL time.Duration
}

// NewIssuer builds an Issuer from the instance private key. The issuer string
// identifies the minting service (the JWT "iss" claim). The signing key's id
// (its RFC 7638 thumbprint) is stamped into every token header so a verifier can
// select the right key across a rotation.
func NewIssuer(priv *rsa.PrivateKey, issuer string, accessTTL, refreshTTL time.Duration) *Issuer {
	if accessTTL <= 0 {
		accessTTL = DefaultAccessTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = DefaultRefreshTTL
	}
	return &Issuer{
		privateKey:      priv,
		kid:             Thumbprint(&priv.PublicKey),
		issuer:          issuer,
		accessTTL:       accessTTL,
		refreshTTL:      refreshTTL,
		scopedAccessTTL: min(DefaultScopedAccessTTL, accessTTL),
	}
}

// ScopedAccessTTL is the lifetime this issuer gives a scoped access token.
func (i *Issuer) ScopedAccessTTL() time.Duration { return i.scopedAccessTTL }

// Kid is the id (RFC 7638 thumbprint) of this issuer's signing key.
func (i *Issuer) Kid() string { return i.kid }

// ErrNoSessionEpoch is returned by the issue methods that must carry a session epoch
// (refresh and identity tokens) when they are given none. A token minted without one
// could never be exchanged — the issuing service refuses an empty epoch at every
// redemption — so minting it would be a success that fails later, far from its
// cause. Refusing at the mint names the cause where it happens.
var ErrNoSessionEpoch = errors.New("a refresh or identity token requires a session epoch")

// ErrNoSessionID is ErrNoSessionEpoch's counterpart for the session id (the "sid" claim):
// a refresh or identity token minted without one would be refused at every exchange.
var ErrNoSessionID = errors.New("a refresh or identity token requires a session id")

// IssuedToken is a signed token plus the metadata a caller needs to store or
// return it (the JTI for server-side refresh-token tracking, and expiry).
type IssuedToken struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

// tokenSpec is the full set of inputs a signed token carries. Bundled into a
// struct so the issue helpers stay readable as the claim set grows (ADR-033 added
// the identity tier + the superuser marker).
type tokenSpec struct {
	tokenType         string
	tenant            string
	username          string
	email             string
	roles             []string
	authorities       []string
	actingAsSuperuser bool
	// scope and audience are set only on OAuth 2.1 authorization-code tokens
	// (ADR-047): scope is the space-delimited granted scope (carried for audit;
	// authorities are already capped to it), audience is the RFC 8707 resource the
	// token is bound to. Empty on every other token type.
	scope    string
	audience []string
	// clientID is the OAuth 2.1 client a scoped token was minted for (ADR-047),
	// binding the refresh grant to its client. Empty on every other token type.
	clientID string
	// sessionEpoch is the subject identity's session value (the "sep" claim). Set
	// only on refresh and identity tokens; see Claims.SessionEpoch.
	sessionEpoch SessionEpoch
	sessionID    SessionID
	// scopeAnchors is a scoped membership's scope (the "scp" claim, ADR-081). Set only
	// by the two scoped access mints, from a Scope that was validated when it was built.
	scopeAnchors []string
	// amr is the sign-in's authentication methods (the "amr" claim); set only through
	// WithAmr, from the session record.
	amr    []string
	amrSet bool
	jti    string
	ttl    time.Duration
}

// IssueAccess mints a short-lived tenant-scoped access token (the data-plane
// tier). roles are the subject's assigned role names (display/audit); authorities
// are their expanded effective capabilities (what resolvers gate on).
func (i *Issuer) IssueAccess(tenant, username string, roles, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: username,
		roles: roles, authorities: authorities, jti: jti, ttl: i.accessTTL,
	})
}

// IssueTenantAccess mints a tenant-scoped access token for a global identity
// (ADR-033), carrying its email and, when a superuser selected a tenant it holds
// no membership in, the break-glass actingAsSuperuser marker.
func (i *Issuer) IssueTenantAccess(tenant, email string, roles, authorities []string, actingAsSuperuser bool, jti string, opts ...MintOption) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, actingAsSuperuser: actingAsSuperuser,
		jti: jti, ttl: i.accessTTL,
	}, opts...)
}

// ErrUnscopedScope is returned by the scoped mints when given an unscoped Scope. An
// unscoped member is minted with IssueTenantAccess / IssueOAuthAccess, which carry no
// "scp" at all; a scoped mint with nothing to put in it would otherwise write a claim
// the validator refuses, a success that fails at the next request.
var ErrUnscopedScope = errors.New("a scoped access token requires a scoped Scope")

// mintableScope refuses a Scope a scoped mint cannot write faithfully: an unscoped one
// (ErrUnscopedScope), and a scoped one whose anchors are empty or malformed. The second
// matters because the claim is omitempty: a scoped Scope with no anchors — "sees
// nothing", which ScopeFromContext can return — would otherwise be minted with no
// "scp" at all, which is the WHOLE TENANT.
func mintableScope(scope Scope) error {
	if !scope.IsScoped() {
		return ErrUnscopedScope
	}
	return validateAnchors(scope.anchors)
}

// IssueScopedTenantAccess mints the access token of a SCOPED membership (ADR-081): an
// ordinary tenant access token that also carries the membership's scope as the "scp"
// claim and lives for the scoped access TTL. It has no superuser parameter because a
// superuser entering a tenant is never scoped — the absence is the rule, not a default.
// Every resolver that has not opted in refuses the token (Authorize), and core/rdb
// filters what the opted-in ones read.
func (i *Issuer) IssueScopedTenantAccess(tenant, email string, roles, authorities []string, scope Scope, jti string, opts ...MintOption) (IssuedToken, error) {
	if err := mintableScope(scope); err != nil {
		return IssuedToken{}, err
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, scopeAnchors: scope.Anchors(),
		jti: jti, ttl: i.scopedAccessTTL,
	}, opts...)
}

// IssueScopedOAuthAccess is IssueOAuthAccess for a SCOPED membership: the OAuth access
// token an MCP client holds carries the member's scope as well, so the tools — which
// forward the caller's own token — inherit it with no change of their own (ADR-047,
// ADR-081). oauthScope is the OAuth scope string (Claims.Scope); scope is the
// membership's anchors (Claims.ScopeAnchors). Like IssueScopedTenantAccess it has no
// superuser parameter and uses the scoped access TTL.
func (i *Issuer) IssueScopedOAuthAccess(tenant, email string, roles, authorities []string, oauthScope string, audience []string, scope Scope, clientID, jti string, opts ...MintOption) (IssuedToken, error) {
	if err := mintableScope(scope); err != nil {
		return IssuedToken{}, err
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, scope: oauthScope, audience: audience,
		scopeAnchors: scope.Anchors(), clientID: clientID, jti: jti, ttl: i.scopedAccessTTL,
	}, opts...)
}

// IssueOAuthAccess mints a tenant-scoped access token for the OAuth 2.1
// authorization-code flow (ADR-047). It is an ordinary data-plane access token
// (so every existing JWKS validator accepts it unchanged) that additionally
// carries the granted OAuth scope and — when a resource indicator was supplied
// (RFC 8707) — an audience binding. authorities MUST already be capped to the
// scope by the caller via IntersectAuthorities (which caps even AuthorityAll to
// the scope's allowance), so the scope cannot be exceeded even for a subject
// holding "*". This mint does not re-derive the cap — it trusts the caller — so
// the authorization endpoint is the single place the intersection is applied.
func (i *Issuer) IssueOAuthAccess(tenant, email string, roles, authorities []string, scope string, audience []string, actingAsSuperuser bool, clientID, jti string, opts ...MintOption) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, actingAsSuperuser: actingAsSuperuser,
		scope: scope, audience: audience, clientID: clientID, jti: jti, ttl: i.accessTTL,
	}, opts...)
}

// ServiceAccountSubjectPrefix begins the subject ("sub" and "username") of every token
// minted for a tenant's service account (the client_credentials grant). It has no "@", so a
// service account's subject can never equal a person's, whose subject is an email address.
const ServiceAccountSubjectPrefix = "service-account:"

// ServiceAccountSubject is the subject a service account's tokens carry, and so the actor
// the audit journal records for what it does.
func ServiceAccountSubject(accountToken string) string {
	return ServiceAccountSubjectPrefix + accountToken
}

// IssueServiceAccountAccess mints the access token of a tenant's SERVICE ACCOUNT, the answer
// to an OAuth 2.1 client_credentials grant. It is an ordinary data-plane access token —
// every service accepts it unchanged — whose subject is the account (ServiceAccountSubject)
// rather than a person: it carries no email and is never a superuser's. authorities are the
// account's roles' authorities, already capped by the caller to oauthScope when one was
// requested. scope is the account's scope: an unscoped Scope mints no "scp" at all, a scoped
// one is validated exactly as IssueScopedTenantAccess validates it.
//
// It lives for the SCOPED access TTL whether or not the account is scoped. There is no
// refresh token for an account, so this lifetime is how long a disable, a delete, a role
// change or the end of a secret's overlap can take to reach a token already issued.
func (i *Issuer) IssueServiceAccountAccess(tenant, subject string, roles, authorities []string, oauthScope string, audience []string, scope Scope, clientID, jti string) (IssuedToken, error) {
	if tenant == "" || subject == "" || clientID == "" {
		return IssuedToken{}, errors.New("a service account token requires a tenant, a subject and a client id")
	}
	var anchors []string
	if scope.IsScoped() {
		if err := mintableScope(scope); err != nil {
			return IssuedToken{}, err
		}
		anchors = scope.Anchors()
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: subject,
		roles: roles, authorities: authorities, scope: oauthScope, audience: audience,
		scopeAnchors: anchors, clientID: clientID, jti: jti, ttl: i.scopedAccessTTL,
	})
}

// IssueOAuthRefresh mints an OAuth 2.1 refresh token carrying the granted scope
// and audience so a refresh grant re-mints an access token with the same
// scope/audience binding without re-consulting the authorize step. Like every
// refresh token its jti is persisted server-side (NATS KV) for revocation, and it
// carries the subject's session epoch so a credential change ends it. An empty
// epoch is refused (ErrNoSessionEpoch).
func (i *Issuer) IssueOAuthRefresh(tenant, email string, epoch SessionEpoch, sid SessionID, roles, authorities []string, scope string, audience []string, clientID, jti string, opts ...MintOption) (IssuedToken, error) {
	if epoch == "" {
		return IssuedToken{}, ErrNoSessionEpoch
	}
	if sid == "" {
		return IssuedToken{}, ErrNoSessionID
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeRefresh, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, sessionEpoch: epoch, sessionID: sid,
		scope: scope, audience: audience, clientID: clientID, jti: jti, ttl: i.refreshTTL,
	}, opts...)
}

// IssueRefresh mints a long-lived tenant-scoped refresh token. The caller
// persists jti in the refresh-token store (NATS KV) so it can be revoked. It
// carries the subject's session epoch so a credential change ends it; an empty
// epoch is refused (ErrNoSessionEpoch).
func (i *Issuer) IssueRefresh(tenant, username string, epoch SessionEpoch, sid SessionID, roles, authorities []string, jti string, opts ...MintOption) (IssuedToken, error) {
	if epoch == "" {
		return IssuedToken{}, ErrNoSessionEpoch
	}
	if sid == "" {
		return IssuedToken{}, ErrNoSessionID
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeRefresh, tenant: tenant, username: username,
		roles: roles, authorities: authorities, sessionEpoch: epoch, sessionID: sid, jti: jti, ttl: i.refreshTTL,
	}, opts...)
}

// IssueIdentity mints an instance-scoped identity token (ADR-033): no tenant, the
// subject's *system* authorities, used for the admin API and the tenant-selection
// exchange. The data-plane validator rejects it because it carries no tenant. It
// carries the subject's session epoch, which the issuing service checks when the
// token is exchanged for a tenant session, a membership list or an authorization
// code; an empty epoch is refused (ErrNoSessionEpoch). The admin API does NOT check
// the epoch — it verifies the signature only — so an identity token already issued
// keeps its admin authority until it expires (the access-token lifetime).
func (i *Issuer) IssueIdentity(email string, epoch SessionEpoch, sid SessionID, roles, authorities []string, jti string, opts ...MintOption) (IssuedToken, error) {
	if epoch == "" {
		return IssuedToken{}, ErrNoSessionEpoch
	}
	if sid == "" {
		return IssuedToken{}, ErrNoSessionID
	}
	return i.sign(tokenSpec{
		tokenType: TokenTypeIdentity, username: email, email: email,
		roles: roles, authorities: authorities, sessionEpoch: epoch, sessionID: sid, jti: jti, ttl: i.accessTTL,
	}, opts...)
}

// IssueService mints a short-lived instance-scoped *service* token (ADR-044
// amendment): no tenant, the requested machine authorities, signed by the same
// instance key so every service's existing JWKS validator accepts it with no new
// trust configuration. subject names the calling service (audit). The data-plane
// validator rejects it as an access token because it carries no tenant; a callee
// admits it through the service tier and takes the tenant from an explicit header.
func (i *Issuer) IssueService(subject string, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeService, username: subject,
		authorities: authorities, jti: jti, ttl: ServiceTokenTTL,
	})
}

func (i *Issuer) sign(spec tokenSpec, opts ...MintOption) (IssuedToken, error) {
	for _, o := range opts {
		o(&spec)
	}
	if spec.amrSet && !validAmrShape(spec.amr) {
		return IssuedToken{}, fmt.Errorf("%w: %v", ErrMalformedAmr, spec.amr)
	}
	now := time.Now()
	expires := now.Add(spec.ttl)
	claims := &Claims{
		Tenant:            spec.tenant,
		Username:          spec.username,
		Email:             spec.email,
		Roles:             spec.roles,
		Authorities:       spec.authorities,
		ActingAsSuperuser: spec.actingAsSuperuser,
		Scope:             spec.scope,
		ScopeAnchors:      spec.scopeAnchors,
		Amr:               spec.amr,
		ClientId:          spec.clientID,
		SessionEpoch:      spec.sessionEpoch,
		SessionID:         spec.sessionID,
		TokenType:         spec.tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Subject:   spec.username,
			Audience:  jwt.ClaimStrings(spec.audience),
			ID:        spec.jti,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = i.kid
	signed, err := token.SignedString(i.privateKey)
	if err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: signed, ID: spec.jti, ExpiresAt: expires}, nil
}
