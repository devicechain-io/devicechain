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
	// ErrScopeUnsupported is the refusal a SCOPED caller (ADR-081) meets at any
	// operation that has not opted in to scoped callers: at Authorize/AuthorizeAny, at
	// core/rdb's scope callback for a model that declares no scope rule, and at a
	// service-to-service call made on its behalf. It IS an ErrForbidden
	// (errors.Is(err, ErrForbidden) holds), so every caller that already handles a
	// refusal handles this one, and the wire answers it FORBIDDEN with the reason
	// SCOPE_UNSUPPORTED. Its message starts with "forbidden" for the same reason the
	// clients match on that prefix.
	ErrScopeUnsupported error = scopeUnsupported{}
)

// scopeUnsupported is ErrScopeUnsupported's type: a distinct sentinel that also
// reports itself as ErrForbidden.
type scopeUnsupported struct{}

func (scopeUnsupported) Error() string {
	return "forbidden: this operation is not available to a member whose access is limited to part of the tenant"
}

// Is makes ErrScopeUnsupported an ErrForbidden to errors.Is.
func (scopeUnsupported) Is(target error) bool { return target == ErrForbidden }

// ReasonScopeUnsupported is the wire reason (extensions.reason) of an
// ErrScopeUnsupported refusal.
const ReasonScopeUnsupported = "SCOPE_UNSUPPORTED"

// ForbiddenReason returns the wire reason of a refusal, or "" when it carries none.
// It is the one mapping from a refusal's sentinel to its published reason, read by the
// GraphQL boundary.
func ForbiddenReason(err error) string {
	if errors.Is(err, ErrScopeUnsupported) {
		return ReasonScopeUnsupported
	}
	return ""
}

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
// there. An authority declaring BOTH tiers (audit:read) is satisfiable here, which
// is why dual-tier is reserved for capabilities that genuinely exist on both planes.
//
// "*" is exempt from the lookup by construction — it has no tier of its own (see
// AuthorityAll) and is bounded by this same rule via the required authority it is
// being tested against, not by itself.
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
	tiers, known := TiersOf(required)
	return known && tiers.Has(TierTenant)
}

// Authorize enforces that the request's verified claims grant the required
// authority (ADR-008 RBAC) at a tier the claims' token may carry it on (ADR-065).
// It is the single capability check every service's resolvers call: claims are
// attached by the shared auth middleware (WithClaims), so this works uniformly
// across services. Returns ErrUnauthenticated when the request was
// unauthenticated and ErrForbidden when the subject lacks the authority or holds
// it on the wrong tier.
//
// 🔴 IT REFUSES EVERY SCOPED CALLER (ADR-081), with ErrScopeUnsupported, whatever
// authorities the token holds. That is the deny-by-default half of scoped memberships:
// every resolver written before scopes existed — hundreds of call sites — answers a
// customer user FORBIDDEN with no edit, and a resolver written later fails closed until
// it opts in. Opting in is switching to AuthorizeScoped (and applying the scope it
// returns) or AuthorizeCatalogue; there is no other door.
func Authorize(ctx context.Context, required Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if scopeOf(claims).IsScoped() {
		return ErrScopeUnsupported
	}
	if !satisfies(claims, required) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeService is Authorize for an operation that only another SERVICE may call: the
// caller must hold a service token (TokenTypeService), and that token must carry the
// authority. A tenant user's access token is refused with ErrForbidden even when it holds
// the authority, because the operation answers a question (does this secret belong to this
// integration?) that a human session has no business asking. Only a holder of the shared
// service secret can mint a service token, so no listener of its own is needed.
//
// Like Authorize it refuses every scoped caller with ErrScopeUnsupported, and answers
// ErrUnauthenticated when there are no claims.
func AuthorizeService(ctx context.Context, required Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if scopeOf(claims).IsScoped() {
		return ErrScopeUnsupported
	}
	if claims.TokenType != TokenTypeService {
		return ErrForbidden
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
// passing an empty set. Like Authorize, it refuses every scoped caller.
func AuthorizeAny(ctx context.Context, required ...Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if scopeOf(claims).IsScoped() {
		return ErrScopeUnsupported
	}
	for _, authority := range required {
		if satisfies(claims, authority) {
			return nil
		}
	}
	return ErrForbidden
}

// AuthorizeScoped is Authorize for a resolver that ADMITS scoped callers (ADR-081). It
// checks the authority exactly as Authorize does — tier included — and returns the
// caller's scope instead of refusing a scoped one: the zero Scope for an unscoped
// caller, a scoped one otherwise.
//
// Calling it is a promise the resolver keeps: every row it returns is filtered by the
// scope. For a model read through gorm that is done by core/rdb's scope callback, which
// applies the model's ScopedBy rule — and refuses a model that declares none — with no
// further code here. A resolver that reaches rows any other way (raw SQL, a projection
// struct, another service) applies the returned scope itself, and carries a
// cross-customer test that proves it. A by-token read of a row outside the scope answers
// NOT_FOUND, never FORBIDDEN: a scoped caller must not learn that a row it may not see
// exists.
func AuthorizeScoped(ctx context.Context, required Authority) (Scope, error) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return Scope{}, ErrUnauthenticated
	}
	if !satisfies(claims, required) {
		return Scope{}, ErrForbidden
	}
	return scopeOf(claims), nil
}

// AuthorizeCatalogue is Authorize for a tenant CATALOGUE read that is safe for any
// member, scoped or not (ADR-081): tenant configuration that is not device-derived —
// device types, profile versions and their metric and command vocabularies, units —
// which a customer's console needs to render what it is allowed to see. It checks the
// authority exactly as Authorize does and admits a scoped caller unfiltered.
//
// Use it only for reads of configuration. A resolver that returns a device-derived row
// through it hands a scoped caller the whole tenant; core/rdb still refuses a scoped
// read of any model that does not declare rdb.Catalogue() or another rule, so the
// mistake surfaces as a refusal at the model rather than as a leak.
func AuthorizeCatalogue(ctx context.Context, required Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !satisfies(claims, required) {
		return ErrForbidden
	}
	return nil
}
