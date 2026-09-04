// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new geofence. A geofence is an authored tenant resource, so it takes the
// same device:write authority as the profiles and rules it is authored alongside — AND
// location:read, for the reason set out on requireFenceAuthoring below.
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
	if err := requireFenceAuthoring(ctx); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateGeoFence(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &GeoFenceResolver{M: *created, S: r, C: ctx}, nil
}

// requireFenceAuthoring is the authority pair for MINTING or MOVING a fence: device:write,
// because a fence is an authored tenant resource, AND location:read.
//
// 🔴🔴 THE SECOND ONE IS NOT BELT-AND-BRACES, IT CLOSES A PRIVILEGE INVERSION. A fence is a
// QUESTION ABOUT WHERE A DEVICE IS, and whoever can shape the question can extract the answer:
// a caller holding device:write but not location:read could mint a fence a few metres across,
// preview or publish a rule testing containment, read the raise/resolve edge, and repeat —
// binary-searching a device's actual coordinates out of a system that had just refused to show
// them one. That recovers POSITION ITSELF, not merely containment against a region someone else
// drew, and it made device:write strictly out-read the authority invented to gate position.
//
// Gating the read paths alone could not fix this. The preview gate is on the DRAFT (it fires
// when the compiled leaf calls the containment predicate), which stops that caller reading a
// containment timeline — but a fence outlives the preview, and the same shrinking trick works
// against any surface that reports a fence rule's firings. The authority has to attach where the
// question is CONSTRUCTED, which is here.
//
// READING fences deliberately stays on device:read. A fence definition is a region someone drew
// on a map — a tenant resource, not a device's position — and putting location:read on the list
// would make the map unusable for the operators who maintain it while protecting nothing: the
// geometry says nothing about where any device actually is.
func requireFenceAuthoring(ctx context.Context) error {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return err
	}
	return auth.Authorize(ctx, auth.LocationRead)
}

// Update an existing geofence. Same authority pair as creation, and for the same reason: moving
// or reshaping an existing fence is the same question-shaping primitive as minting a new one, so
// gating creation alone would leave the inversion open to anyone who could edit one fence.
func (r *SchemaResolver) UpdateGeoFence(ctx context.Context, args struct {
	Token   string
	Request model.GeoFenceUpdateRequest
}) (*GeoFenceResolver, error) {
	if err := requireFenceAuthoring(ctx); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateGeoFence(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &GeoFenceResolver{M: *updated, S: r, C: ctx}, nil
}
