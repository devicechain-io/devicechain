// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new geofence. A geofence is an authored tenant resource, so it takes the
// same device:write authority as the profiles and rules it is authored alongside.
//
// This uses the plain (uncached) API like every other authoring mutation: the cache
// eviction a fence change requires runs through Api.CacheEvictor, which the wiring sets
// to the caching decorator — so the eviction is a property of the API call, not of which
// decorator the resolver happened to reach for. That is deliberately unlike profile
// publish/rollback, which reach for GetCachedApi because their eviction lives on the
// decorator's own method.
func (r *SchemaResolver) CreateGeoFence(ctx context.Context, args struct {
	Request model.GeoFenceCreateRequest
}) (*GeoFenceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateGeoFence(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &GeoFenceResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing geofence.
func (r *SchemaResolver) UpdateGeoFence(ctx context.Context, args struct {
	Token   string
	Request model.GeoFenceCreateRequest
}) (*GeoFenceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateGeoFence(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &GeoFenceResolver{M: *updated, S: r, C: ctx}, nil
}
