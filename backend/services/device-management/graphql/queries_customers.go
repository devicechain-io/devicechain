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
	_ "embed"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find customer types by unique id.
func (r *SchemaResolver) CustomerTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerTypeResolver, error) {
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

// Find customer relationship types by unique id.
func (r *SchemaResolver) CustomerRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomerRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer relationship types by unique token.
func (r *SchemaResolver) CustomerRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer relationship types that match the given criteria.
func (r *SchemaResolver) CustomerRelationshipTypes(ctx context.Context, args struct {
	Criteria model.CustomerRelationshipTypeSearchCriteria
}) (*CustomerRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find customer relationships by unique id.
func (r *SchemaResolver) CustomerRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomerRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &CustomerRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer relationships by unique token.
func (r *SchemaResolver) CustomerRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &CustomerRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer relationships that match the given criteria.
func (r *SchemaResolver) CustomerRelationships(ctx context.Context, args struct {
	Criteria model.CustomerRelationshipSearchCriteria
}) (*CustomerRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find customer groups by unique id.
func (r *SchemaResolver) CustomerGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerGroupResolver, error) {
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

// Find customer group relationship types by unique id.
func (r *SchemaResolver) CustomerGroupRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomerGroupRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer group relationship types by unique token.
func (r *SchemaResolver) CustomerGroupRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerGroupRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer group relationship types that match the given criteria.
func (r *SchemaResolver) CustomerGroupRelationshipTypes(ctx context.Context, args struct {
	Criteria model.CustomerGroupRelationshipTypeSearchCriteria
}) (*CustomerGroupRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerGroupRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerGroupRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find customer group relationships by unique id.
func (r *SchemaResolver) CustomerGroupRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CustomerGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CustomerGroupRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find customer group relationships by unique token.
func (r *SchemaResolver) CustomerGroupRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CustomerGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerGroupRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CustomerGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &CustomerGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all customer group relationships that match the given criteria.
func (r *SchemaResolver) CustomerGroupRelationships(ctx context.Context, args struct {
	Criteria model.CustomerGroupRelationshipSearchCriteria
}) (*CustomerGroupRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CustomerGroupRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CustomerGroupRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
