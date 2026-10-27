// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"math"

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

// Remove the unregistered devices an integration has recorded.
func (r *SchemaResolver) ClearUnregisteredIntegrationDevices(ctx context.Context, args struct {
	Integration string
}) (int32, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return 0, err
	}
	n, err := r.GetApi(ctx).ClearUnregisteredIntegrationDevices(ctx, args.Integration)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	return int32(n), nil
}

// Turn a sender's identifier into a device (service plane). The caller is a SERVICE: a service
// token holding device:read, and a user's access token is refused even when it holds the
// authority, because the operation answers a question (does this secret belong to this
// integration?) that a human session has no business asking.
//
// It is a mutation because it records a sighting when no device in scope carries the alias, and
// it asks for device:read, not device:write, on purpose: the service token that asks is the
// ingest path's, which must not hold the authority to change or delete devices. The only write
// is an observation row, capped per integration and not journalled.
func (r *SchemaResolver) ResolveIntegrationDevice(ctx context.Context, args struct {
	Integration string
	Secret      string
	Scheme      string
	Value       string
}) (*IntegrationDeviceResolutionResolver, error) {
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	resolved, err := r.GetApi(ctx).ResolveIntegrationDevice(ctx, args.Integration, args.Secret, args.Scheme, args.Value)
	if err != nil {
		return nil, err
	}
	return &IntegrationDeviceResolutionResolver{M: resolved}, nil
}
