// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a new device profile.
func (r *SchemaResolver) CreateDeviceProfile(ctx context.Context, args struct {
	Request *model.DeviceProfileCreateRequest
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDeviceProfile(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing device profile.
func (r *SchemaResolver) UpdateDeviceProfile(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceProfileCreateRequest
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceProfile(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *updated, S: r, C: ctx}, nil
}
