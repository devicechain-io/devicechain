// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
)

// Find trust anchors by unique id.
func (r *SchemaResolver) DeviceTrustAnchorsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).DeviceTrustAnchorsById(ctx, ids)
	if err != nil {
		return nil, err
	}
	return r.anchorResolvers(ctx, found), nil
}

// trustAnchorByID resolves the anchor a credential or a profile names, for a caller holding
// device:read WITHOUT a scope. A scoped caller is refused with an error rather than handed nil,
// so the field never answers "none" about an anchor that exists.
func (r *SchemaResolver) trustAnchorByID(ctx context.Context, id uint) (*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).DeviceTrustAnchorsById(ctx, []uint{id})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &DeviceTrustAnchorResolver{M: *found[0], S: r, C: ctx}, nil
}

// Find trust anchors by unique token.
func (r *SchemaResolver) DeviceTrustAnchorsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).DeviceTrustAnchorsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	return r.anchorResolvers(ctx, found), nil
}

// List trust anchors that meet criteria.
func (r *SchemaResolver) DeviceTrustAnchors(ctx context.Context, args struct {
	Criteria model.DeviceTrustAnchorSearchCriteria
}) (*DeviceTrustAnchorSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	results, err := r.GetApi(ctx).DeviceTrustAnchors(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &DeviceTrustAnchorSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}

func (r *SchemaResolver) anchorResolvers(ctx context.Context, found []*model.DeviceTrustAnchor) []*DeviceTrustAnchorResolver {
	out := make([]*DeviceTrustAnchorResolver, 0, len(found))
	for _, a := range found {
		out = append(out, &DeviceTrustAnchorResolver{M: *a, S: r, C: ctx})
	}
	return out
}
