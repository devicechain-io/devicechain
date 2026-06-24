/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new customer type.
func (r *SchemaResolver) CreateCustomerType(ctx context.Context, args struct {
	Request *model.CustomerTypeCreateRequest
}) (*CustomerTypeResolver, error) {
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

// Create a new customer relationship type.
func (r *SchemaResolver) CreateCustomerRelationshipType(ctx context.Context, args struct {
	Request *model.CustomerRelationshipTypeCreateRequest
}) (*CustomerRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateCustomerRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing customer relationship type.
func (r *SchemaResolver) UpdateCustomerRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.CustomerRelationshipTypeCreateRequest
}) (*CustomerRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomerRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new customer relationship.
func (r *SchemaResolver) CreateCustomerRelationship(ctx context.Context, args struct {
	Request *model.CustomerRelationshipCreateRequest
}) (*CustomerRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateCustomerRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new customer group.
func (r *SchemaResolver) CreateCustomerGroup(ctx context.Context, args struct {
	Request *model.CustomerGroupCreateRequest
}) (*CustomerGroupResolver, error) {
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

// Create a new customer group relationship type.
func (r *SchemaResolver) CreateCustomerGroupRelationshipType(ctx context.Context, args struct {
	Request *model.CustomerGroupRelationshipTypeCreateRequest
}) (*CustomerGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateCustomerGroupRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerGroupRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing customer group relationship type.
func (r *SchemaResolver) UpdateCustomerGroupRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.CustomerGroupRelationshipTypeCreateRequest
}) (*CustomerGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateCustomerGroupRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerGroupRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new customer group relationship.
func (r *SchemaResolver) CreateCustomerGroupRelationship(ctx context.Context, args struct {
	Request *model.CustomerGroupRelationshipCreateRequest
}) (*CustomerGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateCustomerGroupRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &CustomerGroupRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}
