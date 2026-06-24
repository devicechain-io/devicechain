// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new device type.
func (r *SchemaResolver) CreateDeviceType(ctx context.Context, args struct {
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device type.
func (r *SchemaResolver) UpdateDeviceType(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device.
func (r *SchemaResolver) CreateDevice(ctx context.Context, args struct {
	Request *model.DeviceCreateRequest
}) (*DeviceResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDevice(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device.
func (r *SchemaResolver) UpdateDevice(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceCreateRequest
}) (*DeviceResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDevice(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device group.
func (r *SchemaResolver) CreateDeviceGroup(ctx context.Context, args struct {
	Request *model.DeviceGroupCreateRequest
}) (*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device type.
func (r *SchemaResolver) UpdateDeviceGroup(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceGroupCreateRequest
}) (*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
