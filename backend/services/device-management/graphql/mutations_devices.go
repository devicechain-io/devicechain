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

// Create a new device type.
func (r *SchemaResolver) CreateDeviceType(ctx context.Context, args struct {
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device type.
func (r *SchemaResolver) UpdateDeviceType(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device.
func (r *SchemaResolver) CreateDevice(ctx context.Context, args struct {
	Request *model.DeviceCreateRequest
}) (*DeviceResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDevice(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device.
func (r *SchemaResolver) UpdateDevice(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceCreateRequest
}) (*DeviceResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDevice(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device relationship type.
func (r *SchemaResolver) CreateDeviceRelationshipType(ctx context.Context, args struct {
	Request *model.DeviceRelationshipTypeCreateRequest
}) (*DeviceRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device relationship type.
func (r *SchemaResolver) UpdateDeviceRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceRelationshipTypeCreateRequest
}) (*DeviceRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device relationship.
func (r *SchemaResolver) CreateDeviceRelationship(ctx context.Context, args struct {
	Request *model.DeviceRelationshipCreateRequest
}) (*DeviceRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device group.
func (r *SchemaResolver) CreateDeviceGroup(ctx context.Context, args struct {
	Request *model.DeviceGroupCreateRequest
}) (*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceGroup(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device type.
func (r *SchemaResolver) UpdateDeviceGroup(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceGroupCreateRequest
}) (*DeviceGroupResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceGroup(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device group relationship type.
func (r *SchemaResolver) CreateDeviceGroupRelationshipType(ctx context.Context, args struct {
	Request *model.DeviceGroupRelationshipTypeCreateRequest
}) (*DeviceGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceGroupRelationshipType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupRelationshipTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device group relationship type.
func (r *SchemaResolver) UpdateDeviceGroupRelationshipType(ctx context.Context, args struct {
	Token   string
	Request *model.DeviceGroupRelationshipTypeCreateRequest
}) (*DeviceGroupRelationshipTypeResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceGroupRelationshipType(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupRelationshipTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device group relationship.
func (r *SchemaResolver) CreateDeviceGroupRelationship(ctx context.Context, args struct {
	Request *model.DeviceGroupRelationshipCreateRequest
}) (*DeviceGroupRelationshipResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateDeviceGroupRelationship(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceGroupRelationshipResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}
