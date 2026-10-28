// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a trust anchor from a CA certificate.
func (r *SchemaResolver) CreateDeviceTrustAnchor(ctx context.Context, args struct {
	Request model.DeviceTrustAnchorCreateRequest
}) (*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateDeviceTrustAnchor(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceTrustAnchorResolver{M: *created, S: r, C: ctx}, nil
}

// Update a trust anchor. Disabling one ends the live sessions of the devices that rely on it.
func (r *SchemaResolver) UpdateDeviceTrustAnchor(ctx context.Context, args struct {
	Token   string
	Request model.DeviceTrustAnchorUpdateRequest
}) (*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).UpdateDeviceTrustAnchor(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceTrustAnchorResolver{M: *updated, S: r, C: ctx}, nil
}

// Delete a trust anchor nothing relies on.
func (r *SchemaResolver) DeleteDeviceTrustAnchor(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceTrustAnchor(ctx, args.Token)
}
