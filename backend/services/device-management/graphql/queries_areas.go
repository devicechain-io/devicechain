// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find area types by unique id.
func (r *SchemaResolver) AreaTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.AreaTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area types by unique token.
func (r *SchemaResolver) AreaTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AreaTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area types that match the given criteria.
func (r *SchemaResolver) AreaTypes(ctx context.Context, args struct {
	Criteria model.AreaTypeSearchCriteria
}) (*AreaTypeSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AreaTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find areas by unique id.
func (r *SchemaResolver) AreasById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreasById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaResolver, 0)
	for _, dt := range found {
		dtr := &AreaResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find areas by unique token.
func (r *SchemaResolver) AreasByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AreasByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaResolver, 0)
	for _, dt := range found {
		dtr := &AreaResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all areas that match the given criteria.
func (r *SchemaResolver) Areas(ctx context.Context, args struct {
	Criteria model.AreaSearchCriteria
}) (*AreaSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Areas(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find area groups by unique id.
func (r *SchemaResolver) AreaGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreaGroupsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area groups by unique token.
func (r *SchemaResolver) AreaGroupsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AreaGroupsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area groups that match the given criteria.
func (r *SchemaResolver) AreaGroups(ctx context.Context, args struct {
	Criteria model.AreaGroupSearchCriteria
}) (*AreaGroupSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AreaGroups(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaGroupSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
