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

// bearerContextKey carries the caller's own raw access token beside its verified claims.
type bearerContextKey struct{}

// WithBearer returns a context carrying the raw bearer token the claims on it were verified
// from. Set only by the auth middleware, in the same step as WithClaims.
//
// 🔴 IT EXISTS FOR ONE KIND OF CALL: a request made ON THE CALLER'S BEHALF, UNDER THE CALLER'S
// OWN TOKEN, to another area that then applies the caller's own authority (an agent action's
// gate asking user-management for the tenant's verdict, agentic actions). It is never a
// credential this process may use for its own purposes, it is never logged, and it never
// outlives the request: anything that would need it later must ask again under a request of its
// own.
func WithBearer(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, bearerContextKey{}, token)
}

// BearerFromContext returns the caller's raw bearer token, or ("", false) when the request
// carried none.
func BearerFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	t, ok := ctx.Value(bearerContextKey{}).(string)
	return t, ok && t != ""
}
