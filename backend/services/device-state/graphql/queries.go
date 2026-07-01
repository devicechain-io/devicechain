// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-state/model"
)

// Find device states by originating device id.
func (r *SchemaResolver) DeviceStatesByDeviceId(ctx context.Context, args struct {
	DeviceIds []int32
}) ([]*DeviceStateResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids := make([]uint, 0, len(args.DeviceIds))
	for _, id := range args.DeviceIds {
		ids = append(ids, uint(id))
	}

	found, err := api.DeviceStatesByDeviceId(ctx, ids)
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
	DeviceId int32
}) ([]*LatestMeasurementResolver, error) {
	if err := auth.Authorize(ctx, auth.StateRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.LatestMeasurementsByDeviceId(ctx, uint(args.DeviceId))
	if err != nil {
		return nil, err
	}

	result := make([]*LatestMeasurementResolver, 0)
	for _, m := range found {
		result = append(result, &LatestMeasurementResolver{M: *m, S: r, C: ctx})
	}
	return result, nil
}
