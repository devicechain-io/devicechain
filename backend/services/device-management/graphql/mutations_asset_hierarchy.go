// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// SetAssetParent places an asset under a parent, replacing whatever parent it had
// (ADR-072). Gated on device:write, matching every other asset mutation.
//
// It returns the EntityRelationship it created rather than the asset, and that is
// deliberate: the hierarchy IS an edge of the reserved "contains" type, with no
// parent column on Asset to return. Handing back the asset would suggest the parent
// lives on it.
func (r *SchemaResolver) SetAssetParent(ctx context.Context, args struct {
	ChildToken  string
	ParentToken string
}) (*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.SetAssetParent(ctx, args.ChildToken, args.ParentToken)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipResolver{M: *created, S: r, C: ctx}, nil
}

// ClearAssetParent detaches an asset from its parent, making it a root. Its own
// children travel with it — this moves one edge, not a subtree.
func (r *SchemaResolver) ClearAssetParent(ctx context.Context, args struct {
	ChildToken string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}

	api := r.GetApi(ctx)
	return api.ClearAssetParent(ctx, args.ChildToken)
}
