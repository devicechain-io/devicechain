// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find asset types by unique id.
func (r *SchemaResolver) AssetTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.AssetTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset types by unique token.
func (r *SchemaResolver) AssetTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset types that match the given criteria.
func (r *SchemaResolver) AssetTypes(ctx context.Context, args struct {
	Criteria model.AssetTypeSearchCriteria
}) (*AssetTypeSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find assets by unique id.
func (r *SchemaResolver) AssetsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetResolver, 0)
	for _, dt := range found {
		dtr := &AssetResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find assets by unique token.
func (r *SchemaResolver) AssetsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssetsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetResolver, 0)
	for _, dt := range found {
		dtr := &AssetResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all assets that match the given criteria.
func (r *SchemaResolver) Assets(ctx context.Context, args struct {
	Criteria model.AssetSearchCriteria
}) (*AssetSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Assets(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
