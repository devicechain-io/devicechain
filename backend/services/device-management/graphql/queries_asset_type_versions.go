// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// AssetTypeVersions lists an asset type's published property-contract versions,
// newest first (ADR-072).
func (r *SchemaResolver) AssetTypeVersions(ctx context.Context, args struct {
	Token string
}) ([]*AssetTypeVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	versions, err := api.AssetTypeVersions(ctx, args.Token)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetTypeVersionResolver, 0, len(versions))
	for _, v := range versions {
		result = append(result, &AssetTypeVersionResolver{M: *v, S: r, C: ctx})
	}
	return result, nil
}

// ActiveAssetTypeVersion returns the version an asset of this type is currently
// validated against, or null when the type has never been published.
//
// It exists as its own door rather than being read off assetTypeVersions because
// the console needs the CONTRACT to render a property form, and picking "the active
// one" out of a version list means the client re-deriving what active means from
// AssetType.activeVersion — a second implementation of the pointer, in a place a
// dangling pointer would silently read as "no schema" instead of as an error.
func (r *SchemaResolver) ActiveAssetTypeVersion(ctx context.Context, args struct {
	Token string
}) (*AssetTypeVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	version, err := api.ActiveAssetTypeVersion(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, nil
	}
	return &AssetTypeVersionResolver{M: *version, S: r, C: ctx}, nil
}
