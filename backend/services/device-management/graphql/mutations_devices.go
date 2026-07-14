// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new device type.
func (r *SchemaResolver) CreateDeviceType(ctx context.Context, args struct {
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

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
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	// Route through the caching decorator: attaching/detaching/changing the
	// profile (ADR-045) changes what the ingest path resolves for this type, so
	// the update must evict the type's cached metric definitions.
	api := r.GetCachedApi(ctx)
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
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

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
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

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
