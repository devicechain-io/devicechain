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

// Create a new asset type.
func (r *SchemaResolver) CreateAssetType(ctx context.Context, args struct {
	Request *model.AssetTypeCreateRequest
}) (*AssetTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset type.
func (r *SchemaResolver) UpdateAssetType(ctx context.Context, args struct {
	Token   string
	Request *model.AssetTypeCreateRequest
}) (*AssetTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAssetType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset.
func (r *SchemaResolver) CreateAsset(ctx context.Context, args struct {
	Request *model.AssetCreateRequest
}) (*AssetResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAsset(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset.
func (r *SchemaResolver) UpdateAsset(ctx context.Context, args struct {
	Token   string
	Request *model.AssetCreateRequest
}) (*AssetResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAsset(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset relationship type.
func (r *SchemaResolver) CreateAssetRelationshipType(ctx context.Context, args struct {
	Request *model.AssetRelationshipTypeCreateRequest
}) (*AssetRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset relationship type.
func (r *SchemaResolver) UpdateAssetRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.AssetRelationshipTypeCreateRequest
}) (*AssetRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAssetRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset relationship.
func (r *SchemaResolver) CreateAssetRelationship(ctx context.Context, args struct {
	Request *model.AssetRelationshipCreateRequest
}) (*AssetRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset group.
func (r *SchemaResolver) CreateAssetGroup(ctx context.Context, args struct {
	Request *model.AssetGroupCreateRequest
}) (*AssetGroupResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset type.
func (r *SchemaResolver) UpdateAssetGroup(ctx context.Context, args struct {
	Token   string
	Request *model.AssetGroupCreateRequest
}) (*AssetGroupResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAssetGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset group relationship type.
func (r *SchemaResolver) CreateAssetGroupRelationshipType(ctx context.Context, args struct {
	Request *model.AssetGroupRelationshipTypeCreateRequest
}) (*AssetGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetGroupRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetGroupRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing asset group relationship type.
func (r *SchemaResolver) UpdateAssetGroupRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.AssetGroupRelationshipTypeCreateRequest
}) (*AssetGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateAssetGroupRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetGroupRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new asset group relationship.
func (r *SchemaResolver) CreateAssetGroupRelationship(ctx context.Context, args struct {
	Request *model.AssetGroupRelationshipCreateRequest
}) (*AssetGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateAssetGroupRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AssetGroupRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}
