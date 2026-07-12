// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "github.com/golang-jwt/jwt/v5"

// Token type values carried in the "typ" claim. Access tokens authorize
// tenant-scoped data-plane calls; refresh tokens are exchanged at the
// user-management /refresh mutation and never accepted by the request
// middleware. Identity tokens are the instance-scoped tier (ADR-033): they carry
// no tenant, carry the subject's *system* authorities, and authorize the admin
// API + the tenant-selection exchange — the data-plane validator rejects them
// precisely because they have no tenant claim.
// Service tokens (ADR-044 amendment) are the instance-scoped *machine* tier: a
// short-lived credential one service presents on a synchronous cross-service call
// (core/svcclient). Like an identity token they carry no tenant — a service acts
// across tenants on the platform's behalf and passes the tenant explicitly — but
// they carry the caller's requested authorities so the callee still gates each
// operation. They are minted only by user-management, only on presentation of the
// shared service secret, so a client cannot obtain one.
const (
	TokenTypeAccess   = "access"
	TokenTypeRefresh  = "refresh"
	TokenTypeIdentity = "identity"
	TokenTypeService  = "service"
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
	// Email is the global identity's email (ADR-033). Carried on identity tokens
	// (and tenant tokens minted from one) as the stable cross-tenant principal id.
	Email string `json:"email,omitempty"`
	// ActingAsSuperuser marks a tenant token minted for a superuser who selected a
	// tenant they hold no membership in (break-glass). Carried for audit so the
	// log shows a superuser acted, not a phantom tenant-admin.
	ActingAsSuperuser bool `json:"sudo,omitempty"`
	// Roles are the subject's assigned role names. Carried for display/audit;
	// enforcement is on Authorities, not role names (ADR-008 RBAC).
	Roles []string `json:"roles,omitempty"`
	// Authorities are the subject's effective capabilities — the union of their
	// roles' granted authorities, expanded at token-issue time. Resolvers gate on
	// these (see Authorize); a holder of AuthorityAll ("*") passes every check.
	// A token minted through the OAuth 2.1 authorization-code flow (ADR-047) carries
	// the *intersection* of the subject's authorities with what the granted Scope
	// permits — never a superset — so an OAuth session cannot exceed its scope even
	// for a subject who holds "*".
	Authorities []string `json:"authorities,omitempty"`
	// Scope is the space-delimited OAuth 2.1 scope set granted to a token minted via
	// the authorization-code flow (ADR-047); empty on every non-OAuth token. It is
	// carried for audit/introspection — enforcement is on Authorities, which are
	// already capped to the scope at issue time. The audience a scoped token is
	// bound to (RFC 8707) rides the standard "aud" registered claim.
	Scope string `json:"scope,omitempty"`
	// TokenType distinguishes access from refresh tokens (see TokenType*).
	TokenType string `json:"typ"`

	jwt.RegisteredClaims
}

// HasAuthority reports whether the claims grant the required authority. A subject
// holding the super-authority AuthorityAll ("*") passes every check.
func (c *Claims) HasAuthority(required Authority) bool {
	for _, held := range c.Authorities {
		if held == string(AuthorityAll) || held == string(required) {
			return true
		}
	}
	return false
}
