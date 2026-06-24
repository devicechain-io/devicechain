// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

// claimsContextKey is unexported so only this package can set verified claims on
// a context — a resolver can read them but cannot forge them.
type claimsContextKey struct{}

// WithClaims returns a context carrying the verified JWT claims. Set only by the
// auth middleware after successful signature verification.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext returns the verified claims attached by the auth middleware,
// or (nil, false) when the request was unauthenticated.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	if ctx == nil {
		return nil, false
	}
	claims, ok := ctx.Value(claimsContextKey{}).(*Claims)
	return claims, ok && claims != nil
}
