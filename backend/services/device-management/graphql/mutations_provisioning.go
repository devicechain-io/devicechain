// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a new provisioning profile.
func (r *SchemaResolver) CreateProvisioningProfile(ctx context.Context, args struct {
	Request *model.ProvisioningProfileCreateRequest
}) (*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateProvisioningProfile(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing provisioning profile.
func (r *SchemaResolver) UpdateProvisioningProfile(ctx context.Context, args struct {
	Token   string
	Request *model.ProvisioningProfileCreateRequest
}) (*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateProvisioningProfile(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *updated, S: r, C: ctx}, nil
}

// NOTE: device self-registration has no GraphQL surface. The api-layer flow
// (model.Api.ProvisionDevice / ProvisionDeviceBootstrap) is retained as the
// onboarding primitive a future device-plane provisioning transport will call
// (ADR-012); the ADR-025 broker path, not GraphQL, is where device-facing entry
// points live.
