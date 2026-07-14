// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find device type by unique id.
func (r *SchemaResolver) DeviceTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.DeviceTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceTypeResolver, 0)
	for _, dt := range found {
		dtr := &DeviceTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find device type by unique token.
func (r *SchemaResolver) DeviceTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceTypeResolver, 0)
	for _, dt := range found {
		dtr := &DeviceTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all device types that match the given criteria.
func (r *SchemaResolver) DeviceTypes(ctx context.Context, args struct {
	Criteria model.DeviceTypeSearchCriteria
}) (*DeviceTypeSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find devices by unique id.
func (r *SchemaResolver) DevicesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DevicesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// Find devices by unique token.
func (r *SchemaResolver) DevicesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DevicesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// Find devices by their customer-owned external id (ADR-049).
func (r *SchemaResolver) DevicesByExternalId(ctx context.Context, args struct {
	ExternalIds []string
}) ([]*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DevicesByExternalId(ctx, args.ExternalIds)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// List all devices that match the given criteria.
func (r *SchemaResolver) Devices(ctx context.Context, args struct {
	Criteria model.DeviceSearchCriteria
}) (*DeviceSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Devices(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
