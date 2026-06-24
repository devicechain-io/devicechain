// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "github.com/golang-jwt/jwt/v5"

// Token type values carried in the "typ" claim. Access tokens authorize API
// calls; refresh tokens are exchanged at the user-management /refresh mutation
// for a new access token and are never accepted by the request middleware.
const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

// Claims is the DeviceChain JWT payload (ADR-008). The Tenant claim is the
// authoritative source of request tenancy, replacing the temporary trusted
// X-DC-Tenant gateway header: a verified token's tenant cannot be forged by a
// client because the signature covers it.
type Claims struct {
	// Tenant is the tenant the subject is acting within. Stamped into the
	// request context (core.WithTenant) so the fail-closed DB scope applies.
	Tenant string `json:"tenant"`
	// Username is the human-readable subject identifier (mirrors sub).
	Username string `json:"username"`
	// Roles are granted authority names. Carried for forward authorization
	// work (Phase 2); the auth middleware does not yet gate on them.
	Roles []string `json:"roles,omitempty"`
	// TokenType distinguishes access from refresh tokens (see TokenType*).
	TokenType string `json:"typ"`

	jwt.RegisteredClaims
}
