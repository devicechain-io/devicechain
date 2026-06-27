// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find customer types by unique id.
func (r *SchemaResolver) CustomerTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.CustomerTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer types by unique token.
func (r *SchemaResolver) CustomerTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CustomerTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer types that match the given criteria.
func (r *SchemaResolver) CustomerTypes(ctx context.Context, args struct {
	Criteria model.CustomerTypeSearchCriteria
}) (*CustomerTypeSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CustomerTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find customers by unique id.
func (r *SchemaResolver) CustomersById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomersById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerResolver, 0)
	for _, dt := range found {
		dtr := &CustomerResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customers by unique token.
func (r *SchemaResolver) CustomersByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CustomersByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerResolver, 0)
	for _, dt := range found {
		dtr := &CustomerResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customers that match the given criteria.
func (r *SchemaResolver) Customers(ctx context.Context, args struct {
	Criteria model.CustomerSearchCriteria
}) (*CustomerSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Customers(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find customer groups by unique id.
func (r *SchemaResolver) CustomerGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomerGroupsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer groups by unique token.
func (r *SchemaResolver) CustomerGroupsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CustomerGroupsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer groups that match the given criteria.
func (r *SchemaResolver) CustomerGroups(ctx context.Context, args struct {
	Criteria model.CustomerGroupSearchCriteria
}) (*CustomerGroupSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CustomerGroups(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerGroupSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
