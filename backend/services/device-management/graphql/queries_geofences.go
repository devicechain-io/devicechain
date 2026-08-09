// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find geofences by unique id.
func (r *SchemaResolver) GeoFencesById(ctx context.Context, args struct {
	Ids []string
}) ([]*GeoFenceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.GeoFencesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*GeoFenceResolver, 0)
	for _, gf := range found {
		result = append(result, &GeoFenceResolver{M: *gf, S: r, C: ctx})
	}
	return result, nil
}

// Find geofences by unique token.
func (r *SchemaResolver) GeoFencesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*GeoFenceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.GeoFencesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*GeoFenceResolver, 0)
	for _, gf := range found {
		result = append(result, &GeoFenceResolver{M: *gf, S: r, C: ctx})
	}
	return result, nil
}

// List all geofences that match the given criteria.
func (r *SchemaResolver) GeoFences(ctx context.Context, args struct {
	Criteria model.GeoFenceSearchCriteria
}) (*GeoFenceSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.GeoFences(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &GeoFenceSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// CurrentFenceSetVersion exposes the tenant's active fence-set version — the number
// stamped onto resolved location events. It is a read of an authored resource's state,
// so it carries the same device:read authority as the fences themselves.
func (r *SchemaResolver) CurrentFenceSetVersion(ctx context.Context) (int32, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return 0, err
	}
	return r.GetApi(ctx).CurrentFenceSetVersion(ctx)
}
