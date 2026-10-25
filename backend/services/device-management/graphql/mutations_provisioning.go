// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a new provisioning profile. The response carries the provision secret the server
// generated for it, once.
func (r *SchemaResolver) CreateProvisioningProfile(ctx context.Context, args struct {
	Request *model.ProvisioningProfileCreateRequest
}) (*ProvisioningProfileSecretResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateProvisioningProfile(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileSecretResultResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing provisioning profile.
func (r *SchemaResolver) UpdateProvisioningProfile(ctx context.Context, args struct {
	Token   string
	Request model.ProvisioningProfileUpdateRequest
}) (*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateProvisioningProfile(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *updated, S: r, C: ctx}, nil
}

// Replace a provisioning profile's secret with a new one, returned once.
func (r *SchemaResolver) RotateProvisioningProfileSecret(ctx context.Context, args struct {
	Token string
}) (*ProvisioningProfileSecretResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	rotated, err := r.GetApi(ctx).RotateProvisioningProfileSecret(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileSecretResultResolver{M: *rotated, S: r, C: ctx}, nil
}

// Disable every credential a provisioning profile issued and end those devices' sessions.
func (r *SchemaResolver) RevokeProvisionedDevices(ctx context.Context, args struct {
	ProfileToken string
}) (int32, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return 0, err
	}

	n, err := r.GetApi(ctx).RevokeProvisionedDevices(ctx, args.ProfileToken)
	return int32(n), err
}

// Delete the credentials self-registration issued to a device and end its sessions.
func (r *SchemaResolver) ResetDeviceProvisioning(ctx context.Context, args struct {
	DeviceToken string
}) (int32, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return 0, err
	}

	n, err := r.GetApi(ctx).ResetDeviceProvisioning(ctx, args.DeviceToken)
	return int32(n), err
}

// NOTE: the registration a device makes is not a GraphQL operation. It is the HTTP endpoint
// POST /provisioning/{tenant}/register, which authenticates with the profile's key and secret
// and calls model.Api.ProvisionDevice; there is no user behind it, so there is no authority to
// check here.
