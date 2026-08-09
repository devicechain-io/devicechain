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

// GeoFenceSetSnapshot exposes the FROZEN fence set of one fence-set version — the fences
// that were live when an event stamped with that version was resolved (ADR-078).
//
// This is the door event-processing resolves a version its live projection no longer holds
// through: an authoring preview replaying last week, a late redelivery, a cold start. That
// projection retains only a handful of recent versions on purpose (it is a cache, not an
// archive); THIS service is the archive, and this query is how the archive is reachable.
//
// 🔴 IT TAKES NO TENANT, AND THAT IS THE TENANCY MECHANISM. The fence-set rows are
// tenant-scoped, so the read is confined by the tenant in the caller's context (the DB
// scope callback refuses a tenant-scoped query with no tenant at all). There is no
// parameter through which a caller could name a tenant, so asking for another tenant's
// fence set is not a request that can be expressed — not a check that could be forgotten.
// A version belonging to another tenant is simply not on record here, which surfaces as
// "not found", the same answer a version that never existed gets.
//
// It carries device:read, matching every other geofence read: a fence's geometry is where
// a tenant's sites are, so the frozen set is exactly as sensitive as the live one.
func (r *SchemaResolver) GeoFenceSetSnapshot(ctx context.Context, args struct {
	Version int32
}) (*GeoFenceSetSnapshotResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).GeoFenceSetSnapshotAt(ctx, args.Version)
	if err != nil {
		return nil, err
	}
	return &GeoFenceSetSnapshotResolver{M: *found, S: r, C: ctx}, nil
}

// CurrentGeoFenceSet exposes the frozen fence set of the tenant's CURRENT version — the
// version and the fences together, read from one row so the two cannot disagree.
//
// It is what event-processing's startup reconcile seeds its live projection from, so a
// restarted engine is not blind to a tenant's fences until the next fence edit. Tenancy
// and authority are exactly as for geoFenceSetSnapshot above.
func (r *SchemaResolver) CurrentGeoFenceSet(ctx context.Context) (*GeoFenceSetSnapshotResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &GeoFenceSetSnapshotResolver{M: *found, S: r, C: ctx}, nil
}
