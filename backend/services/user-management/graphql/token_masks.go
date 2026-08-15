// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
)

// tokenMasks reads the effective entity token-mask map as a JSON object string.
//
// Authentication only, no authority: the masks are non-sensitive UI templates and
// every console user needs them to mint a token in a create form. Requiring
// settings:read would make token generation an admin-only convenience, which is
// the opposite of what it is for.
//
// 🔴 ONE implementation, deliberately, called from BOTH planes — the tenant data
// plane (SchemaResolver) and the identity-lane settings schema (SettingsResolver).
// The two exist because a screen can only ask over the session it holds, and the
// two sessions have wildly different lifetimes: a tenant session lasts days, the
// identity token fifteen minutes and cannot refresh. Serving the masks on one
// plane only means one class of screen reads them over a lane that is dead, gets
// an auth error, and falls back to the built-in pattern — silently minting tokens
// that ignore the operator's configuration.
//
// Sharing the body is what makes "both planes answer identically" a property of
// the code rather than a coincidence two resolvers happen to maintain. A future
// change to the masks contract cannot land on one plane and miss the other.
//
// 🔑 It reads the settings service itself, AFTER the auth check, rather than being
// handed one. Both planes fetch it identically (settingsFromContext), and that
// assertion panics when the key is absent — so a caller that resolved the service
// to pass it in would evaluate it before the refusal and turn an unauthenticated
// request into a panic. Fetching it here means the ordering cannot be got wrong by
// a caller at all, instead of being a rule the callers have to keep.
func tokenMasks(ctx context.Context) (string, error) {
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return "", auth.ErrUnauthenticated
	}
	eff, err := settingsFromContext(ctx).Get(ctx, settingsdefs.KeyTokenMasks)
	if err != nil {
		return "", err
	}
	return string(eff.Value), nil
}

// TokenMasks resolves the token masks on the TENANT DATA PLANE, for the create
// forms of tenant-scoped screens — which hold a long-lived tenant session and, past
// its first fifteen minutes, no usable identity token.
//
// Reading an instance-global setting from a tenant-scoped request is already
// established here: the branding cascade does it (see branding.go), for the same
// reason it is safe — the settings store carries no tenant scope, so there is no
// cross-tenant read to make.
func (r *SchemaResolver) TokenMasks(ctx context.Context) (string, error) {
	return tokenMasks(ctx)
}
