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

// IssueAccess mints a short-lived access token for the subject in the tenant.
// roles are the subject's assigned role names (carried for display/audit);
// authorities are their expanded effective capabilities (what resolvers gate on).
func (i *Issuer) IssueAccess(tenant, username string, roles, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(TokenTypeAccess, tenant, username, roles, authorities, jti, i.accessTTL)
}

// IssueRefresh mints a long-lived refresh token. The caller persists jti in the
// refresh-token store (NATS KV) so the token can be validated and revoked.
func (i *Issuer) IssueRefresh(tenant, username string, roles, authorities []string, jti string) (IssuedToken, error) {
	return i.sign(TokenTypeRefresh, tenant, username, roles, authorities, jti, i.refreshTTL)
}

func (i *Issuer) sign(tokenType, tenant, username string, roles, authorities []string, jti string, ttl time.Duration) (IssuedToken, error) {
	now := time.Now()
	expires := now.Add(ttl)
	claims := &Claims{
		Tenant:      tenant,
		Username:    username,
		Roles:       roles,
		Authorities: authorities,
		TokenType:   tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Subject:   username,
			ID:        jti,
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
	return IssuedToken{Token: signed, ID: jti, ExpiresAt: expires}, nil
}
