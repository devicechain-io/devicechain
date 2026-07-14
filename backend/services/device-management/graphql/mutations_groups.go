// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new entity group (ADR-061). Validation (member family, membership
// mode) lives in the model API; v1 admits static groups only.
func (r *SchemaResolver) CreateEntityGroup(ctx context.Context, args struct {
	Request *model.EntityGroupCreateRequest
}) (*EntityGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateEntityGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	return &EntityGroupResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing entity group's presentation fields.
func (r *SchemaResolver) UpdateEntityGroup(ctx context.Context, args struct {
	Token   string
	Request *model.EntityGroupCreateRequest
}) (*EntityGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateEntityGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	return &EntityGroupResolver{M: *updated, S: r, C: ctx}, nil
}
