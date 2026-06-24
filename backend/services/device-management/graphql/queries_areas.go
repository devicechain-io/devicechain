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

// Find area types by unique id.
func (r *SchemaResolver) AreaTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaTypeResolver, error) {
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

// Find area relationship types by unique id.
func (r *SchemaResolver) AreaRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreaRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area relationship types by unique token.
func (r *SchemaResolver) AreaRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area relationship types that match the given criteria.
func (r *SchemaResolver) AreaRelationshipTypes(ctx context.Context, args struct {
	Criteria model.AreaRelationshipTypeSearchCriteria
}) (*AreaRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find area relationships by unique id.
func (r *SchemaResolver) AreaRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreaRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AreaRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area relationships by unique token.
func (r *SchemaResolver) AreaRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AreaRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area relationships that match the given criteria.
func (r *SchemaResolver) AreaRelationships(ctx context.Context, args struct {
	Criteria model.AreaRelationshipSearchCriteria
}) (*AreaRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find area groups by unique id.
func (r *SchemaResolver) AreaGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaGroupResolver, error) {
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

// Find area group relationship types by unique id.
func (r *SchemaResolver) AreaGroupRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreaGroupRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area group relationship types by unique token.
func (r *SchemaResolver) AreaGroupRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaGroupRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area group relationship types that match the given criteria.
func (r *SchemaResolver) AreaGroupRelationshipTypes(ctx context.Context, args struct {
	Criteria model.AreaGroupRelationshipTypeSearchCriteria
}) (*AreaGroupRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaGroupRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaGroupRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find area group relationships by unique id.
func (r *SchemaResolver) AreaGroupRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AreaGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AreaGroupRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find area group relationships by unique token.
func (r *SchemaResolver) AreaGroupRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AreaGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaGroupRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AreaGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AreaGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all area group relationships that match the given criteria.
func (r *SchemaResolver) AreaGroupRelationships(ctx context.Context, args struct {
	Criteria model.AreaGroupRelationshipSearchCriteria
}) (*AreaGroupRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AreaGroupRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AreaGroupRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
