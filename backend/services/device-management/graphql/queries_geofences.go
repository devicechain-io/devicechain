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

// GeoFenceSetManifest exposes the MANIFEST of one fence-set version — which fences it held
// and the content address of each one's geometry, without reading any geometry at all.
//
// It is the read half of manifest delivery, and it is what makes a fence-set version
// resolvable at a cost that does not depend on what the fences contain: an engine that
// missed the announcement, one starting cold, or a preview replaying last week learns WHAT
// to resolve here and fetches only the bodies it does not already hold through
// geoFenceGeometry.
//
// Tenancy and authority are exactly as for geoFenceSetSnapshot: there is no parameter
// through which a caller could name a tenant, the rows are tenant-scoped, and the scope
// callback refuses a tenant-scoped query with no tenant at all — so another tenant's
// manifest is not a request that can be expressed. device:read, matching every other
// geofence read.
func (r *SchemaResolver) GeoFenceSetManifest(ctx context.Context, args struct {
	Version int32
}) (*GeoFenceSetManifestResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).GeoFenceSetManifestAt(ctx, args.Version)
	if err != nil {
		return nil, err
	}
	return &GeoFenceSetManifestResolver{M: *found, S: r, C: ctx}, nil
}

// CurrentGeoFenceSetManifest exposes the manifest of the tenant's CURRENT version — the
// version and its fences read from one row, so the two cannot disagree.
//
// It is what event-processing's startup reconcile and five-minute sweep seed from, so a
// restarted engine is not blind to a tenant's fences until the next fence edit. Tenancy and
// authority are as above.
func (r *SchemaResolver) CurrentGeoFenceSetManifest(ctx context.Context) (*GeoFenceSetManifestResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).CurrentGeoFenceSetManifest(ctx)
	if err != nil {
		return nil, err
	}
	return &GeoFenceSetManifestResolver{M: *found, S: r, C: ctx}, nil
}

// GeoFenceGeometry exposes archived geometry documents by content address — the other half
// of manifest delivery.
//
// 🔴 AN ADDRESS THE TENANT DOES NOT HOLD IS SIMPLY ABSENT FROM THE RESULT. That is a
// deliberate contract and it is why this door is safe to expose: the hashes are
// caller-supplied, so "which of these do you hold?" is an ordinary question whose answer is
// a set. It is also why the CALLER carries a duty this door cannot discharge for it — a
// caller resolving a manifest must turn a missing body into a fence that reports an error,
// never one that is silently absent, because an absent fence reads downstream as "this fence
// does not exist here" and containment then answers "outside" for a device that is inside.
//
// Tenancy is the same mechanism as the manifest doors: the archive rows are tenant-scoped
// and no parameter names a tenant, so a hash belonging to another tenant is not on record
// here and reads exactly like a hash that never existed.
//
// The request length is capped, and over-cap is an ERROR rather than a truncated answer —
// following validateCommandEnqueueBatch, the other door in this schema that bounds a list.
// A caller that asked for forty addresses and silently received twenty-four could not
// distinguish that from a tenant holding only twenty-four of them, which is exactly the
// confusion the absence contract above depends on not existing.
func (r *SchemaResolver) GeoFenceGeometry(ctx context.Context, args struct {
	Hashes []string
}) ([]*GeoFenceGeometryResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).GeoFenceGeometryDocuments(ctx, args.Hashes)
	if err != nil {
		return nil, err
	}
	result := make([]*GeoFenceGeometryResolver, 0, len(found))
	for i := range found {
		result = append(result, &GeoFenceGeometryResolver{M: found[i], S: r, C: ctx})
	}
	return result, nil
}
