// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A membership may carry a SCOPE (ADR-081): a short list of anchors that bounds which
// of the tenant's device-derived rows its holder can see. The scope rides the tenant
// access token as the "scp" claim (Claims.ScopeAnchors) and is read back here, from the
// verified claims, by the two places that enforce it — the resolver gate (Authorize,
// AuthorizeScoped) and core/rdb's scope callback.
//
// 🔑 THERE IS NO WithScope, ON PURPOSE. The scope is read from the verified claims
// already on the context (WithClaims), never from a second context value stamped beside
// them. A second value would be a second answer to "is this caller scoped?", and the two
// could disagree: a transport that attached the claims but forgot to stamp the scope
// would leave Authorize refusing (it reads the claims) while the storage gate filtered
// nothing (it would read the missing value) — a scoped caller reading the whole tenant
// through any resolver that had opted in. Deriving it from the claims leaves nothing to
// forget, on every transport, including the ones written later.

// Anchor kinds a scope may name. They are the entity-type spellings the event pipeline
// already stamps on its anchors (entity.TypeCustomer is "customer"), so an anchor in a
// scope and an anchor on a row compare as plain strings.
const (
	AnchorKindCustomer = "customer"
	AnchorKindGroup    = "group"
)

// MaxScopeAnchors bounds a scope. The bound keeps the token small and every predicate
// built from a scope short. A longer scope is refused, never truncated: a truncated
// scope would silently hide devices the member was granted, and nothing in the answer
// would say so.
const MaxScopeAnchors = 16

// maxAnchorTokenLen mirrors core.MaxTokenLen (128). It is restated rather than imported
// because core imports this package; the full token grammar is enforced where a
// membership's scope is WRITTEN (user-management), which can import core. Here the
// question is only "is this shaped like an anchor", and an anchor that passes it but
// names no real entity matches no row — it can narrow what a scoped caller sees, never
// widen it.
const maxAnchorTokenLen = 128

// ErrMalformedScope is the refusal for a scope that is not a well-formed list of at most
// MaxScopeAnchors anchors, each "customer:<token>" or "group:<token>".
var ErrMalformedScope = errors.New("malformed scope")

// Scope is the scope a caller acts under: either unscoped (the whole tenant, the zero
// value) or scoped to a set of anchors. A scoped Scope with NO anchors is a real state,
// not a synonym for unscoped: it sees nothing. Absence of anchors never means "all".
type Scope struct {
	scoped  bool
	anchors []string
}

// IsScoped reports whether the caller is limited to part of the tenant.
func (s Scope) IsScoped() bool { return s.scoped }

// Anchors returns a copy of the scope's anchors ("customer:acme", ...). It is empty for
// an unscoped caller — check IsScoped, never the length, to tell the two apart.
func (s Scope) Anchors() []string {
	return append([]string(nil), s.anchors...)
}

// NewScope builds a scoped Scope from anchors, refusing a malformed one
// (ErrMalformedScope). An empty list is refused too: a membership with no anchors is an
// UNSCOPED membership, and is minted without the claim, never with an empty one.
func NewScope(anchors []string) (Scope, error) {
	if err := validateAnchors(anchors); err != nil {
		return Scope{}, err
	}
	return Scope{scoped: true, anchors: append([]string(nil), anchors...)}, nil
}

// ParseAnchor splits an anchor into its kind and token, refusing anything else.
func ParseAnchor(anchor string) (kind, token string, err error) {
	kind, token, ok := strings.Cut(anchor, ":")
	if !ok || token == "" {
		return "", "", fmt.Errorf("%w: anchor %q is not <kind>:<token>", ErrMalformedScope, anchor)
	}
	if kind != AnchorKindCustomer && kind != AnchorKindGroup {
		return "", "", fmt.Errorf("%w: anchor %q names an unknown kind", ErrMalformedScope, anchor)
	}
	if len(token) > maxAnchorTokenLen || strings.ContainsAny(token, ": \t\r\n") {
		return "", "", fmt.Errorf("%w: anchor %q has a malformed token", ErrMalformedScope, anchor)
	}
	return kind, token, nil
}

func validateAnchors(anchors []string) error {
	if len(anchors) == 0 {
		return fmt.Errorf("%w: a scope names at least one anchor", ErrMalformedScope)
	}
	if len(anchors) > MaxScopeAnchors {
		return fmt.Errorf("%w: %d anchors exceeds the maximum of %d", ErrMalformedScope, len(anchors), MaxScopeAnchors)
	}
	for _, a := range anchors {
		if _, _, err := ParseAnchor(a); err != nil {
			return err
		}
	}
	return nil
}

// validateScopeClaim is the parse-time rule for the "scp" claim. It is applied to every
// token the validator admits (Validator.Parse), so no transport can skip it.
//
//   - Absent is the ordinary, unscoped case.
//   - Present, it must be well-formed (validateAnchors) — including NON-EMPTY. The issuer
//     never writes an empty "scp" (omitempty), so an empty one was minted by something
//     else, and accepting it would leave its meaning to whoever reads it next.
//   - It rides tenant ACCESS tokens only. An identity or service token carrying it is
//     refused (ADR-081 decision 2): those are the tiers that act above a single tenant,
//     and a scope on one would be either ignored — a confused deputy handed a scoped
//     caller's request with unscoped credentials — or obeyed somewhere it was never
//     meant to reach. A REFRESH token carrying it is refused too: nothing reads a scope
//     from a refresh token, because every refresh re-reads the membership and mints the
//     scope it finds there. A claim no reader consults is a second, stale copy of the
//     answer waiting for the first reader that trusts it — a refresh path that copied
//     the old token's scope forward would carry a narrowed member's wider scope past the
//     narrowing. Refusing it at the parse makes that mistake unwritable. The rule is an
//     ALLOW-LIST, like satisfies(): a token type this build does not recognise is
//     refused rather than admitted.
//   - A superuser is never scoped, so a token carrying both the superuser marker and a
//     scope was not minted by this platform's rules and is refused.
func validateScopeClaim(c *Claims) error {
	if c.ScopeAnchors == nil {
		return nil
	}
	switch c.TokenType {
	case TokenTypeAccess:
	default:
		return fmt.Errorf("%w: a %q token may not carry a scope", ErrMalformedScope, c.TokenType)
	}
	if c.ActingAsSuperuser {
		return fmt.Errorf("%w: a superuser token may not carry a scope", ErrMalformedScope)
	}
	return validateAnchors(c.ScopeAnchors)
}

// scopeOf is the one reading of a claims' scope. A non-nil ScopeAnchors is scoped
// WHATEVER it holds: claims built in-process (a test, a future transport) that never
// went through validateScopeClaim with an empty list still mean "scoped to nothing",
// never "the whole tenant". The fail-closed reading is the only one written down.
func scopeOf(c *Claims) Scope {
	if c == nil || c.ScopeAnchors == nil {
		return Scope{}
	}
	return Scope{scoped: true, anchors: append([]string(nil), c.ScopeAnchors...)}
}

// ScopeFromContext returns the scope of the request's verified claims, and whether the
// caller is scoped. An unauthenticated context, and any caller whose claims carry no
// scope, is unscoped: the tenant predicate alone applies, exactly as before ADR-081.
//
// Service and identity contexts are unscoped by construction rather than by a check
// here: Validator.Parse refuses "scp" on either, so their claims never carry one. The
// enqueue validator's device lookup, which runs under a service token, therefore sees
// the tenant the way it always has.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return Scope{}, false
	}
	s := scopeOf(claims)
	return s, s.scoped
}
