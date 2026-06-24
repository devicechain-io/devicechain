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

// Create a new area type.
func (r *SchemaResolver) CreateAreaType(ctx context.Context, args struct {
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area type.
func (r *SchemaResolver) UpdateAreaType(ctx context.Context, args struct {
	Token   string
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area.
func (r *SchemaResolver) CreateArea(ctx context.Context, args struct {
	Request *model.AreaCreateRequest
}) (*AreaResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateArea(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area.
func (r *SchemaResolver) UpdateArea(ctx context.Context, args struct {
	Token   string
	Request *model.AreaCreateRequest
}) (*AreaResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateArea(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area relationship type.
func (r *SchemaResolver) CreateAreaRelationshipType(ctx context.Context, args struct {
	Request *model.AreaRelationshipTypeCreateRequest
}) (*AreaRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area relationship type.
func (r *SchemaResolver) UpdateAreaRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.AreaRelationshipTypeCreateRequest
}) (*AreaRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area relationship.
func (r *SchemaResolver) CreateAreaRelationship(ctx context.Context, args struct {
	Request *model.AreaRelationshipCreateRequest
}) (*AreaRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area group.
func (r *SchemaResolver) CreateAreaGroup(ctx context.Context, args struct {
	Request *model.AreaGroupCreateRequest
}) (*AreaGroupResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area type.
func (r *SchemaResolver) UpdateAreaGroup(ctx context.Context, args struct {
	Token   string
	Request *model.AreaGroupCreateRequest
}) (*AreaGroupResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area group relationship type.
func (r *SchemaResolver) CreateAreaGroupRelationshipType(ctx context.Context, args struct {
	Request *model.AreaGroupRelationshipTypeCreateRequest
}) (*AreaGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaGroupRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area group relationship type.
func (r *SchemaResolver) UpdateAreaGroupRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.AreaGroupRelationshipTypeCreateRequest
}) (*AreaGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaGroupRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area group relationship.
func (r *SchemaResolver) CreateAreaGroupRelationship(ctx context.Context, args struct {
	Request *model.AreaGroupRelationshipCreateRequest
}) (*AreaGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAreaGroupRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaGroupRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}
