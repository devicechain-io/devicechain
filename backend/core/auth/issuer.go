// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
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
		privateKey: priv,
		kid:        Thumbprint(&priv.PublicKey),
		issuer:     issuer,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// Kid is the id (RFC 7638 thumbprint) of this issuer's signing key.
func (i *Issuer) Kid() string { return i.kid }

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
	jti      string
	ttl      time.Duration
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
func (i *Issuer) IssueTenantAccess(tenant, email string, roles, authorities []string, actingAsSuperuser bool, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, actingAsSuperuser: actingAsSuperuser,
		jti: jti, ttl: i.accessTTL,
	})
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
func (i *Issuer) IssueOAuthAccess(tenant, email string, roles, authorities []string, scope string, audience []string, actingAsSuperuser bool, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeAccess, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities, actingAsSuperuser: actingAsSuperuser,
		scope: scope, audience: audience, jti: jti, ttl: i.accessTTL,
	})
}

// IssueOAuthRefresh mints an OAuth 2.1 refresh token carrying the granted scope
// and audience so a refresh grant re-mints an access token with the same
// scope/audience binding without re-consulting the authorize step. Like every
// refresh token its jti is persisted server-side (NATS KV) for revocation.
func (i *Issuer) IssueOAuthRefresh(tenant, email string, roles, authorities []string, scope string, audience []string, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeRefresh, tenant: tenant, username: email, email: email,
		roles: roles, authorities: authorities,
		scope: scope, audience: audience, jti: jti, ttl: i.refreshTTL,
	})
}

// IssueRefresh mints a long-lived tenant-scoped refresh token. The caller
// persists jti in the refresh-token store (NATS KV) so it can be revoked.
func (i *Issuer) IssueRefresh(tenant, username string, roles, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeRefresh, tenant: tenant, username: username,
		roles: roles, authorities: authorities, jti: jti, ttl: i.refreshTTL,
	})
}

// IssueIdentity mints an instance-scoped identity token (ADR-033): no tenant, the
// subject's *system* authorities, used for the admin API and the tenant-selection
// exchange. The data-plane validator rejects it because it carries no tenant.
func (i *Issuer) IssueIdentity(email string, roles, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(tokenSpec{
		tokenType: TokenTypeIdentity, username: email, email: email,
		roles: roles, authorities: authorities, jti: jti, ttl: i.accessTTL,
	})
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

func (i *Issuer) sign(spec tokenSpec) (IssuedToken, error) {
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
