// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create an inbound integration. The response carries the bearer token the server generated
// for it, once.
func (r *SchemaResolver) CreateIntegration(ctx context.Context, args struct {
	Request model.IntegrationCreateRequest
}) (*IntegrationSecretResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	created, err := r.GetApi(ctx).CreateIntegration(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &IntegrationSecretResultResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing integration.
func (r *SchemaResolver) UpdateIntegration(ctx context.Context, args struct {
	Token   string
	Request model.IntegrationUpdateRequest
}) (*IntegrationResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	updated, err := r.GetApi(ctx).UpdateIntegration(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &IntegrationResolver{M: *updated, S: r, C: ctx}, nil
}

// Replace an integration's bearer token with a new one, returned once.
func (r *SchemaResolver) RotateIntegrationToken(ctx context.Context, args struct {
	Token string
}) (*IntegrationSecretResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	rotated, err := r.GetApi(ctx).RotateIntegrationToken(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &IntegrationSecretResultResolver{M: *rotated, S: r, C: ctx}, nil
}

// Delete an integration.
func (r *SchemaResolver) DeleteIntegration(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteIntegration(ctx, args.Token)
}
