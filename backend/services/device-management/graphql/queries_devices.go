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

// Find device type by unique id.
func (r *SchemaResolver) DeviceTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceTypeResolver, error) {
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

// List all devices that match the given criteria.
func (r *SchemaResolver) Devices(ctx context.Context, args struct {
	Criteria model.DeviceSearchCriteria
}) (*DeviceSearchResultsResolver, error) {
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

// Find device relationship types by unique id.
func (r *SchemaResolver) DeviceRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceRelationshipTypeResolver, 0)
	for _, rec := range found {
		rez := &DeviceRelationshipTypeResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// Find device relationship types by unique token.
func (r *SchemaResolver) DeviceRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceRelationshipTypeResolver, 0)
	for _, rec := range found {
		rez := &DeviceRelationshipTypeResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// List all device relationship types that match the given criteria.
func (r *SchemaResolver) DeviceRelationshipTypes(ctx context.Context, args struct {
	Criteria model.DeviceRelationshipTypeSearchCriteria
}) (*DeviceRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find device relationships by unique id.
func (r *SchemaResolver) DeviceRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceRelationshipResolver, 0)
	for _, rec := range found {
		rez := &DeviceRelationshipResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// Find device relationships by unique token.
func (r *SchemaResolver) DeviceRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceRelationshipResolver, 0)
	for _, rec := range found {
		rez := &DeviceRelationshipResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// List all device relationships that match the given criteria.
func (r *SchemaResolver) DeviceRelationships(ctx context.Context, args struct {
	Criteria model.DeviceRelationshipSearchCriteria
}) (*DeviceRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find device groups by unique id.
func (r *SchemaResolver) DeviceGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceGroupsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// Find device groups by unique token.
func (r *SchemaResolver) DeviceGroupsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroupsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// List all device groups that match the given criteria.
func (r *SchemaResolver) DeviceGroups(ctx context.Context, args struct {
	Criteria model.DeviceGroupSearchCriteria
}) (*DeviceGroupSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroups(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceGroupSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find device group relationship types by unique id.
func (r *SchemaResolver) DeviceGroupRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceGroupRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupRelationshipTypeResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupRelationshipTypeResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// Find device group relationship types by unique token.
func (r *SchemaResolver) DeviceGroupRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroupRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupRelationshipTypeResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupRelationshipTypeResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// List all device group relationship types that match the given criteria.
func (r *SchemaResolver) DeviceGroupRelationshipTypes(ctx context.Context, args struct {
	Criteria model.DeviceGroupRelationshipTypeSearchCriteria
}) (*DeviceGroupRelationshipTypeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroupRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceGroupRelationshipTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find device group relationships by unique id.
func (r *SchemaResolver) DeviceGroupRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceGroupRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupRelationshipResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupRelationshipResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// Find device group relationships by unique token.
func (r *SchemaResolver) DeviceGroupRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroupRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceGroupRelationshipResolver, 0)
	for _, rec := range found {
		rez := &DeviceGroupRelationshipResolver{
			M: *rec,
			S: r,
			C: ctx,
		}
		result = append(result, rez)
	}
	return result, nil
}

// List all device group relationships that match the given criteria.
func (r *SchemaResolver) DeviceGroupRelationships(ctx context.Context, args struct {
	Criteria model.DeviceGroupRelationshipSearchCriteria
}) (*DeviceGroupRelationshipSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.DeviceGroupRelationships(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceGroupRelationshipSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
