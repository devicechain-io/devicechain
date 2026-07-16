// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
)

// Authorization errors. They are sentinels so a resolver (or a shared error
// mapper) can distinguish "not authenticated" from "authenticated but lacking
// the authority" without string matching.
var (
	// ErrUnauthenticated means no verified claims were present on the context —
	// the request carried no (valid) token. Distinct from ErrForbidden so the
	// transport can answer 401 vs 403.
	ErrUnauthenticated = errors.New("unauthenticated: no verified credentials")
	// ErrForbidden means the subject is authenticated but does not hold the
	// required authority.
	ErrForbidden = errors.New("forbidden: missing required authority")
)

// satisfies reports whether claims may be granted the required authority, tier
// included. It is the one place the tier is enforced at check time, so every
// resolver on every service inherits it by calling Authorize/AuthorizeAny.
//
// The tier rule: a TENANT ACCESS token — the credential a human holds while acting
// inside one tenant — can never satisfy a system-tier authority (ADR-065). This is
// the check the flat vocabulary could not make. Without it, tiering ai:admin
// achieves nothing: the seeded `tenant-admin` role grants "*", HasAuthority passes
// "*" for every check, and a superuser breaking glass into a tenant carries "*"
// too — which is precisely how an instance-scoped operator resource became
// reachable from the tenant console.
//
// Identity and service tokens are unaffected. An identity token IS the admin
// plane's credential. A service token is an instance-level machine caller minted
// from the shared service secret with an explicit least-privilege authority list;
// four services read tenant:read that way to resolve a tenant's governance
// ceilings, so binding them to the tenant rule would break the platform's own
// fetches. Their trust boundary is the shared secret, not the tenant.
//
// It fails closed on an unknown authority: an authority absent from the vocabulary
// has no tier, so it cannot be shown to be safe on an access token and is refused
// there. "*" is exempt from the lookup by construction — it has no tier of its own
// (see AuthorityAll) and is bounded by this same rule via the required authority
// it is being tested against, not by itself.
func satisfies(claims *Claims, required Authority) bool {
	if !claims.HasAuthority(required) {
		return false
	}
	switch claims.TokenType {
	case TokenTypeIdentity, TokenTypeService:
		// The two tiers that legitimately act above a single tenant.
		return true
	}
	// Everything else — a tenant access token, a refresh token, or a token type
	// this build does not recognise — is bounded to tenant-tier authorities. The
	// exemption is an ALLOW-LIST rather than "not an access token" so an unset or
	// unknown TokenType denies instead of admitting: a new token tier must opt in
	// here deliberately, not inherit the operator plane by forgetting to.
	tier, known := TierOf(required)
	return known && tier == TierTenant
}

// Authorize enforces that the request's verified claims grant the required
// authority (ADR-008 RBAC) at a tier the claims' token may carry it on (ADR-065).
// It is the single capability check every service's resolvers call: claims are
// attached by the shared auth middleware (WithClaims), so this works uniformly
// across services. Returns ErrUnauthenticated when the request was
// unauthenticated and ErrForbidden when the subject lacks the authority or holds
// it on the wrong tier.
func Authorize(ctx context.Context, required Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !satisfies(claims, required) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeAny enforces that the request's claims grant at least one of the
// required authorities, at a tier the claims' token may carry it on. With no
// authorities supplied it authorizes nothing and returns ErrForbidden
// (fail-closed), so a caller cannot accidentally leave a resolver ungated by
// passing an empty set.
func AuthorizeAny(ctx context.Context, required ...Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	for _, authority := range required {
		if satisfies(claims, authority) {
			return nil
		}
	}
	return ErrForbidden
}
