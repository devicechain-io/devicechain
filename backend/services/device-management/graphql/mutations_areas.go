// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new area type.
func (r *SchemaResolver) CreateAreaType(ctx context.Context, args struct {
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
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
func (r *SchemaResolver) UpdateAreaType(ctx context.Context, args struct {
	Token   string
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaType(ctx, args.Token, args.Request)
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
func (r *SchemaResolver) UpdateArea(ctx context.Context, args struct {
	Token   string
	Request *model.AreaCreateRequest
}) (*AreaResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateArea(ctx, args.Token, args.Request)
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

// Create a new area group.
func (r *SchemaResolver) CreateAreaGroup(ctx context.Context, args struct {
	Request *model.AreaGroupCreateRequest
}) (*AreaGroupResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area type.
func (r *SchemaResolver) UpdateAreaGroup(ctx context.Context, args struct {
	Token   string
	Request *model.AreaGroupCreateRequest
}) (*AreaGroupResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
