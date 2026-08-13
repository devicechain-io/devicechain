// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-state/model"
)

// Find device states by originating device token.
func (r *SchemaResolver) DeviceStatesByDeviceToken(ctx context.Context, args struct {
	DeviceTokens []string
}) ([]*DeviceStateResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceStatesByDeviceToken(ctx, args.DeviceTokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceStateResolver, 0)
	for _, ds := range found {
		result = append(result, &DeviceStateResolver{
			M: *ds,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// Find device states by the device's (denormalized) external id.
func (r *SchemaResolver) DeviceStatesByExternalId(ctx context.Context, args struct {
	ExternalIds []string
}) ([]*DeviceStateResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceStatesByExternalId(ctx, args.ExternalIds)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceStateResolver, 0)
	for _, ds := range found {
		result = append(result, &DeviceStateResolver{M: *ds, S: r, C: ctx})
	}
	return result, nil
}

// AssertedDeviceStates returns the calling tenant's ASSERTED device states for one
// event source — the failover-reconciliation source (ADR-067 SP4b). Tenant scope is
// enforced by the RDB callback from the caller's token, so an adapter only ever sees
// its own tenant's asserted devices. activeOnly is required rather than defaulted; see
// the model method for why the two questions are not interchangeable.
func (r *SchemaResolver) AssertedDeviceStates(ctx context.Context, args struct {
	Source     string
	ActiveOnly bool
}) ([]*DeviceStateResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AssertedDeviceStates(ctx, args.Source, args.ActiveOnly)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceStateResolver, 0)
	for _, ds := range found {
		result = append(result, &DeviceStateResolver{M: *ds, S: r, C: ctx})
	}
	return result, nil
}

// List all device states that match the given criteria.
func (r *SchemaResolver) DeviceStates(ctx context.Context, args struct {
	Criteria model.DeviceStateSearchCriteria
}) (*DeviceStateSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceStates(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceStateSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// LatestMeasurements returns the current value of every measurement name for a
// device (the live-readings projection).
func (r *SchemaResolver) LatestMeasurements(ctx context.Context, args struct {
	DeviceToken string
}) ([]*LatestMeasurementResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.LatestMeasurementsByDeviceToken(ctx, args.DeviceToken)
	if err != nil {
		return nil, err
	}

	result := make([]*LatestMeasurementResolver, 0)
	for _, m := range found {
		result = append(result, &LatestMeasurementResolver{M: *m, S: r, C: ctx})
	}
	return result, nil
}

// LatestLocation returns a device's last-known position, or null when the device has
// never produced a location event.
//
// 🔴 GATED ON location:read, NOT state:read and NOT event:read. Every other query in
// this service reads state:read, so this looks like an inconsistency and is instead the
// point: a device's POSITION is a distinct capability from its telemetry. Knowing where
// a vehicle — or a person — is differs in kind from knowing how warm it is, and the
// split only works while nothing else grants it. location:read is deliberately absent
// from user-management's read-only viewer baseline, so a caller holding the full viewer
// set (device:read, event:read, state:read, command:read, alarm:read) is refused here
// until someone deliberately grants position access.
func (r *SchemaResolver) LatestLocation(ctx context.Context, args struct {
	DeviceToken string
}) (*LatestLocationResolver, error) {
	if err := auth.Authorize(ctx, auth.LocationRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.LatestLocationsByDeviceToken(ctx, []string{args.DeviceToken})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &LatestLocationResolver{M: *found[0], S: r, C: ctx}, nil
}

// LatestLocations returns the last-known position of each of the given devices — the
// batch form, for a fleet map. A device that has never been located is simply absent
// from the result rather than present with null coordinates, so absence means "never
// located" and a row always carries a real fix.
//
// Gated on location:read for the reason spelled out on LatestLocation: the batch door
// onto the same material must carry the same gate, or the split is decorative.
func (r *SchemaResolver) LatestLocations(ctx context.Context, args struct {
	DeviceTokens []string
}) ([]*LatestLocationResolver, error) {
	if err := auth.Authorize(ctx, auth.LocationRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.LatestLocationsByDeviceToken(ctx, args.DeviceTokens)
	if err != nil {
		return nil, err
	}

	result := make([]*LatestLocationResolver, 0)
	for _, l := range found {
		result = append(result, &LatestLocationResolver{M: *l, S: r, C: ctx})
	}
	return result, nil
}
