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
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema and
// is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field for a
// required argument.
//
// 🔴 Non-null refuses a MISSING request (`request: null`), not an EMPTY one. `{}` is a
// perfectly good non-null input object, and it is accepted as a no-op — which is the
// correct reading of "change nothing", and what the harness's
// EmptyRequestChangesNothing asserts. An earlier version of this comment claimed an
// update naming no fields was a caller error; nothing enforced that, and nothing
// should.
func (r *SchemaResolver) UpdateCustomerType(ctx context.Context, args struct {
	Token   string
	Request model.CustomerTypeUpdateRequest
}) (*CustomerTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomerType(ctx, args.Token, &args.Request)
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
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema and
// is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field for a
// required argument.
//
// 🔴 Non-null refuses a MISSING request (`request: null`), not an EMPTY one. `{}` is a
// perfectly good non-null input object, and it is accepted as a no-op — which is the
// correct reading of "change nothing", and what the harness's
// EmptyRequestChangesNothing asserts. An earlier version of this comment claimed an
// update naming no fields was a caller error; nothing enforced that, and nothing
// should.
func (r *SchemaResolver) UpdateCustomer(ctx context.Context, args struct {
	Token   string
	Request model.CustomerUpdateRequest
}) (*CustomerResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomer(ctx, args.Token, &args.Request)
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
