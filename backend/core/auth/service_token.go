// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// The synchronous cross-service call primitive (ADR-044 amendment) reuses the
// existing JWT/JWKS machinery: a caller mints a short-lived service token from
// user-management by presenting the shared service secret, then calls a target
// service's GraphQL endpoint with that token plus an explicit tenant header. This
// file holds the small wire contract shared by the mint endpoint (user-management)
// and the caller (core/svcclient) so neither redeclares it.

const (
	// ServiceTokenPath is the user-management HTTP endpoint that mints a service
	// token in exchange for the shared service secret.
	ServiceTokenPath = "/auth/service-token"

	// ServiceSecretHeader carries the shared service secret on a mint request. It
	// is the bootstrap trust root — a holder can mint any authority — so it travels
	// only from a caller to user-management, never onward to a callee (the callee
	// trusts the minted token's signature, not the secret).
	ServiceSecretHeader = "X-DC-Service-Secret" //nolint:gosec // header name, not a credential

	// ServiceTenantHeader carries the tenant a service call acts within. A service
	// token has no tenant claim, so the caller states the tenant here. It is honored
	// ONLY after the accompanying service token's signature is verified, so — unlike
	// the retired trusted-gateway header — a client cannot forge request tenancy
	// with it.
	ServiceTenantHeader = "X-DC-Tenant"
)

// ServiceTokenRequest is the body a caller POSTs to ServiceTokenPath. Subject
// names the calling service (audit); Authorities are the least-privilege
// capabilities it needs the minted token to carry (e.g. ["device:read"]).
type ServiceTokenRequest struct {
	Subject     string   `json:"subject"`
	Authorities []string `json:"authorities"`
}

// ServiceTokenResponse is the mint endpoint's reply: the signed token and its
// expiry, so the caller can cache it and refresh ahead of expiry.
type ServiceTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"` // Unix seconds
}
