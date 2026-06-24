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

// Find asset types by unique id.
func (r *SchemaResolver) AssetTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.AssetTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset types by unique token.
func (r *SchemaResolver) AssetTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset types that match the given criteria.
func (r *SchemaResolver) AssetTypes(ctx context.Context, args struct {
	Criteria model.AssetTypeSearchCriteria
}) (*AssetTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find assets by unique id.
func (r *SchemaResolver) AssetsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetResolver, 0)
	for _, dt := range found {
		dtr := &AssetResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find assets by unique token.
func (r *SchemaResolver) AssetsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetResolver, 0)
	for _, dt := range found {
		dtr := &AssetResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all assets that match the given criteria.
func (r *SchemaResolver) Assets(ctx context.Context, args struct {
	Criteria model.AssetSearchCriteria
}) (*AssetSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.Assets(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find asset relationship types by unique id.
func (r *SchemaResolver) AssetRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset relationship types by unique token.
func (r *SchemaResolver) AssetRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset relationship types that match the given criteria.
func (r *SchemaResolver) AssetRelationshipTypes(ctx context.Context, args struct {
	Criteria model.AssetRelationshipTypeSearchCriteria
}) (*AssetRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find asset relationships by unique id.
func (r *SchemaResolver) AssetRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AssetRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset relationships by unique token.
func (r *SchemaResolver) AssetRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AssetRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset relationships that match the given criteria.
func (r *SchemaResolver) AssetRelationships(ctx context.Context, args struct {
	Criteria model.AssetRelationshipSearchCriteria
}) (*AssetRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find asset groups by unique id.
func (r *SchemaResolver) AssetGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetGroupResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetGroupsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset groups by unique token.
func (r *SchemaResolver) AssetGroupsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetGroupResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroupsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset groups that match the given criteria.
func (r *SchemaResolver) AssetGroups(ctx context.Context, args struct {
	Criteria model.AssetGroupSearchCriteria
}) (*AssetGroupSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroups(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetGroupSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find asset group relationship types by unique id.
func (r *SchemaResolver) AssetGroupRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetGroupRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset group relationship types by unique token.
func (r *SchemaResolver) AssetGroupRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroupRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupRelationshipTypeResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupRelationshipTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset group relationship types that match the given criteria.
func (r *SchemaResolver) AssetGroupRelationshipTypes(ctx context.Context, args struct {
	Criteria model.AssetGroupRelationshipTypeSearchCriteria
}) (*AssetGroupRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroupRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetGroupRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find asset group relationships by unique id.
func (r *SchemaResolver) AssetGroupRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AssetGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AssetGroupRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find asset group relationships by unique token.
func (r *SchemaResolver) AssetGroupRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AssetGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroupRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AssetGroupRelationshipResolver, 0)
	for _, dt := range found {
		dtr := &AssetGroupRelationshipResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all asset group relationships that match the given criteria.
func (r *SchemaResolver) AssetGroupRelationships(ctx context.Context, args struct {
	Criteria model.AssetGroupRelationshipSearchCriteria
}) (*AssetGroupRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.AssetGroupRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &AssetGroupRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
