// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// publisher is the identity string recorded on a published profile version: the
// caller's username, falling back to email (identity tokens carry email, tenant
// tokens a username). Empty when unauthenticated (the resolver is already
// auth-gated); taken from the authenticated subject, never caller-supplied, so the
// provenance can't be forged.
func publisher(ctx context.Context) string {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return ""
	}
	if claims.Username != "" {
		return claims.Username
	}
	return claims.Email
}

// PublishDeviceProfile freezes the profile's current draft into a new immutable
// version and makes it the version devices resolve (ADR-045 versioning).
func (r *SchemaResolver) PublishDeviceProfile(ctx context.Context, args struct {
	Token       string
	Label       *string
	Description *string
}) (*DeviceProfileVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	// Route through the cached decorator so publishing evicts the shared ingest
	// cache (a publish changes what devices resolve); reads use the plain api.
	api := r.GetCachedApi(ctx)
	version, err := api.PublishDeviceProfile(ctx, args.Token, args.Label, args.Description, publisher(ctx))
	if err != nil {
		return nil, err
	}
	return &DeviceProfileVersionResolver{M: *version, S: r, C: ctx}, nil
}

// RollbackDeviceProfile re-points the profile's active published version at an
// existing version — a non-destructive rollback; the draft is untouched.
func (r *SchemaResolver) RollbackDeviceProfile(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	// Route through the cached decorator so the rollback evicts the shared ingest
	// cache (the active version pointer moved); reads use the plain api.
	api := r.GetCachedApi(ctx)
	updated, err := api.RollbackDeviceProfile(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *updated, S: r, C: ctx}, nil
}
