// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// PublishAssetType freezes an asset type's draft property contract into a new
// immutable version and makes it the one its assets are validated against
// (ADR-072).
//
// It runs on the PLAIN api, not the cached decorator, and that differs from the
// profile and group publishes on purpose rather than by omission: the ingest cache
// holds what a DEVICE resolves — its type, profile, tracked relationships — and an
// asset's property contract is on none of those paths. Routing through the cache
// would evict entries this change cannot affect.
func (r *SchemaResolver) PublishAssetType(ctx context.Context, args struct {
	Token       string
	Label       *string
	Description *string
}) (*AssetTypeVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	version, err := api.PublishAssetType(ctx, args.Token, args.Label, args.Description, publisher(ctx))
	if err != nil {
		return nil, err
	}
	return &AssetTypeVersionResolver{M: *version, S: r, C: ctx}, nil
}

// RollbackAssetType re-points an asset type's active published version at an
// existing version — a non-destructive rollback; the draft is untouched.
func (r *SchemaResolver) RollbackAssetType(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*AssetTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.RollbackAssetType(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &AssetTypeResolver{M: *updated, S: r, C: ctx}, nil
}
