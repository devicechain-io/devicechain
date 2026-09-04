// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The asset-hierarchy read surface (ADR-072). All three are gated on device:read,
// like every other asset query — the hierarchy is organizational structure, not
// credential material, and an operator who can list assets can already see every
// asset these answers name.

// AssetParent returns the asset directly above this one, or null when it is a root.
// Null is the answer for most assets in a flat tenant, so it is a nullable field
// rather than an error: "has no parent" and "does not exist" are different answers
// and the second is still an error.
func (r *SchemaResolver) AssetParent(ctx context.Context, args struct {
	Token string
}) (*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetParent(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, nil
	}
	return &AssetResolver{M: *found, S: r, C: ctx}, nil
}

// AssetAncestors returns the path to the root, NEAREST FIRST. The order is the
// answer, not a detail: a breadcrumb printed root-first when it should be
// nearest-first is wrong in a way that still looks like a breadcrumb.
func (r *SchemaResolver) AssetAncestors(ctx context.Context, args struct {
	Token string
}) ([]*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetAncestors(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*AssetResolver, 0, len(found))
	for _, asset := range found {
		resolvers = append(resolvers, &AssetResolver{M: *asset, S: r, C: ctx})
	}
	return resolvers, nil
}

// AssetChildren pages the assets directly below a parent, or the tree's ROOTS when
// parentToken is omitted. One field for both because a tree browser asks the same
// question at every level and the root level differs only in the predicate.
func (r *SchemaResolver) AssetChildren(ctx context.Context, args struct {
	ParentToken *string
	Pagination  PaginationInput
}) (*AssetSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetChildren(ctx, args.ParentToken, rdbPagination(args.Pagination))
	if err != nil {
		return nil, err
	}
	return &AssetSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
