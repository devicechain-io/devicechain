// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new area type.
func (r *SchemaResolver) CreateAreaType(ctx context.Context, args struct {
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateAreaType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area type.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema
// and is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field
// for a required argument, and an update naming no fields at all is a caller error
// rather than a no-op to absorb silently.
func (r *SchemaResolver) UpdateAreaType(ctx context.Context, args struct {
	Token   string
	Request model.AreaTypeUpdateRequest
}) (*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaType(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area.
func (r *SchemaResolver) CreateArea(ctx context.Context, args struct {
	Request *model.AreaCreateRequest
}) (*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateArea(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema
// and is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field
// for a required argument, and an update naming no fields at all is a caller error
// rather than a no-op to absorb silently.
func (r *SchemaResolver) UpdateArea(ctx context.Context, args struct {
	Token   string
	Request model.AreaUpdateRequest
}) (*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateArea(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
