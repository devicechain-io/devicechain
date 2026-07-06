// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new alarm definition.
func (r *SchemaResolver) CreateAlarmDefinition(ctx context.Context, args struct {
	Request model.AlarmDefinitionCreateRequest
}) (*AlarmDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateAlarmDefinition(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AlarmDefinitionResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing alarm definition.
func (r *SchemaResolver) UpdateAlarmDefinition(ctx context.Context, args struct {
	Token   string
	Request model.AlarmDefinitionCreateRequest
}) (*AlarmDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateAlarmDefinition(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AlarmDefinitionResolver{M: *updated, S: r, C: ctx}, nil
}
