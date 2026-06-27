// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new customer type.
func (r *SchemaResolver) CreateCustomerType(ctx context.Context, args struct {
	Request *model.CustomerTypeCreateRequest
}) (*CustomerTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCustomerType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing customer type.
func (r *SchemaResolver) UpdateCustomerType(ctx context.Context, args struct {
	Token   string
	Request *model.CustomerTypeCreateRequest
}) (*CustomerTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomerType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new customer.
func (r *SchemaResolver) CreateCustomer(ctx context.Context, args struct {
	Request *model.CustomerCreateRequest
}) (*CustomerResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCustomer(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing customer.
func (r *SchemaResolver) UpdateCustomer(ctx context.Context, args struct {
	Token   string
	Request *model.CustomerCreateRequest
}) (*CustomerResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomer(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new customer group.
func (r *SchemaResolver) CreateCustomerGroup(ctx context.Context, args struct {
	Request *model.CustomerGroupCreateRequest
}) (*CustomerGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCustomerGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing customer type.
func (r *SchemaResolver) UpdateCustomerGroup(ctx context.Context, args struct {
	Token   string
	Request *model.CustomerGroupCreateRequest
}) (*CustomerGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomerGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
