// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new device credential.
func (r *SchemaResolver) CreateDeviceCredential(ctx context.Context, args struct {
	Request *model.DeviceCredentialCreateRequest
}) (*DeviceCredentialResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDeviceCredential(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dc := &DeviceCredentialResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dc, nil
}

// Update an existing device credential.
func (r *SchemaResolver) UpdateDeviceCredential(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceCredentialCreateRequest
}) (*DeviceCredentialResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceCredential(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dc := &DeviceCredentialResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dc, nil
}
