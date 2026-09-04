// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new asset type.
func (r *SchemaResolver) CreateAssetType(ctx context.Context, args struct {
	Request *model.AssetTypeCreateRequest
}) (*AssetTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateAssetType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset type.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema
// and is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field
// for a required argument, and an update naming no fields at all is a caller error
// rather than a no-op to absorb silently.
func (r *SchemaResolver) UpdateAssetType(ctx context.Context, args struct {
	Token   string
	Request model.AssetTypeUpdateRequest
}) (*AssetTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateAssetType(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset.
func (r *SchemaResolver) CreateAsset(ctx context.Context, args struct {
	Request *model.AssetCreateRequest
}) (*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateAsset(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema
// and is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field
// for a required argument, and an update naming no fields at all is a caller error
// rather than a no-op to absorb silently.
func (r *SchemaResolver) UpdateAsset(ctx context.Context, args struct {
	Token   string
	Request model.AssetUpdateRequest
}) (*AssetResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateAsset(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
