// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new command definition.
func (r *SchemaResolver) CreateCommandDefinition(ctx context.Context, args struct {
	Request *model.CommandDefinitionCreateRequest
}) (*CommandDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCommandDefinition(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	cd := &CommandDefinitionResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return cd, nil
}

// Update an existing command definition.
func (r *SchemaResolver) UpdateCommandDefinition(ctx context.Context, args struct {
	Token   string
	Request *model.CommandDefinitionCreateRequest
}) (*CommandDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCommandDefinition(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	cd := &CommandDefinitionResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return cd, nil
}
