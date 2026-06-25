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

// Authorize enforces that the request's verified claims grant the required
// authority (ADR-008 RBAC). It is the single capability check every service's
// resolvers call: claims are attached by the shared auth middleware
// (WithClaims), so this works uniformly across services. Returns
// ErrUnauthenticated when the request was unauthenticated and ErrForbidden when
// the subject lacks the authority.
func Authorize(ctx context.Context, required Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !claims.HasAuthority(required) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeAny enforces that the request's claims grant at least one of the
// required authorities. With no authorities supplied it authorizes nothing and
// returns ErrForbidden (fail-closed), so a caller cannot accidentally leave a
// resolver ungated by passing an empty set.
func AuthorizeAny(ctx context.Context, required ...Authority) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	for _, authority := range required {
		if claims.HasAuthority(authority) {
			return nil
		}
	}
	return ErrForbidden
}
