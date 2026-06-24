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

package gqlclient

import (
	"context"

	"github.com/Khan/genqlient/graphql"
	"github.com/devicechain-io/dc-device-management/model"
)

// Assure that a device type exists.
func AssureDeviceType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceTypeCreateRequest,
) (IDeviceType, bool, error) {
	gresp, err := GetDeviceTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device type.
func CreateDeviceType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceTypeCreateRequest,
) (IDeviceType, error) {
	cresp, err := createDeviceType(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceType, nil
}

// Get device types by token.
func GetDeviceTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceType, error) {
	gresp, err := getDeviceTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceType)
	if gresp != nil {
		for _, res := range gresp.DeviceTypesByToken {
			itypes[res.Token] = IDeviceType(&res)
		}
	}
	return itypes, nil
}

// List device types based on criteria.
func ListDeviceTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IDeviceType, *DefaultPagination, error) {
	resp, err := listDeviceTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceType, 0)
	for _, res := range resp.DeviceTypes.Results {
		results = append(results, IDeviceType(&res.DefaultDeviceType))
	}
	return results, &resp.DeviceTypes.Pagination.DefaultPagination, nil
}

// Assure that a device exists.
func AssureDevice(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceCreateRequest,
) (IDevice, bool, error) {
	gresp, err := GetDevicesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDevice(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device.
func CreateDevice(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceCreateRequest,
) (IDevice, error) {
	cresp, err := createDevice(ctx, client, request.Token, request.DeviceTypeToken,
		request.Name, request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDevice, nil
}

// Get devices by token.
func GetDevicesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDevice, error) {
	gresp, err := getDevicesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDevice)
	if gresp != nil {
		for _, res := range gresp.DevicesByToken {
			itypes[res.Token] = IDevice(&res)
		}
	}
	return itypes, nil
}

// List devices based on criteria.
func ListDevices(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IDevice, *DefaultPagination, error) {
	resp, err := listDevices(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDevice, 0)
	for _, res := range resp.Devices.Results {
		results = append(results, IDevice(&res.DefaultDevice))
	}
	return results, &resp.Devices.Pagination.DefaultPagination, nil
}

// Assure that a device relationship type exists.
func AssureDeviceRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceRelationshipTypeCreateRequest,
) (IDeviceRelationshipType, bool, error) {
	gresp, err := GetDeviceRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device relationship type.
func CreateDeviceRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceRelationshipTypeCreateRequest,
) (IDeviceRelationshipType, error) {
	cresp, err := createDeviceRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata, request.Tracked)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceRelationshipType, nil
}

// Get device relationship types by token.
func GetDeviceRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceRelationshipType, error) {
	gresp, err := getDeviceRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceRelationshipType)
	if gresp != nil {
		for _, res := range gresp.DeviceRelationshipTypesByToken {
			itypes[res.Token] = IDeviceRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List device relationship types based on criteria.
func ListDeviceRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IDeviceRelationshipType, *DefaultPagination, error) {
	resp, err := listDeviceRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceRelationshipType, 0)
	for _, res := range resp.DeviceRelationshipTypes.Results {
		results = append(results, IDeviceRelationshipType(&res.DefaultDeviceRelationshipType))
	}
	return results, &resp.DeviceRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a device relationship exists.
func AssureDeviceRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceRelationshipCreateRequest,
) (IDeviceRelationship, bool, error) {
	gresp, err := GetDeviceRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device relationship.
func CreateDeviceRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceRelationshipCreateRequest,
) (IDeviceRelationship, error) {
	cresp, err := createDeviceRelationship(ctx, client, request.Token, request.SourceDevice,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceRelationship, nil
}

// Get device relationships by token.
func GetDeviceRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceRelationship, error) {
	gresp, err := getDeviceRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceRelationship)
	if gresp != nil {
		for _, res := range gresp.DeviceRelationshipsByToken {
			itypes[res.Token] = IDeviceRelationship(&res)
		}
	}
	return itypes, nil
}

// List device relationships based on criteria.
func ListDeviceRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceDevice *string,
	relationshipType *string,
	tracked *bool,
) ([]IDeviceRelationship, *DefaultPagination, error) {
	resp, err := listDeviceRelationships(ctx, client, pageNumber, pageSize, sourceDevice, relationshipType, tracked)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceRelationship, 0)
	for _, res := range resp.DeviceRelationships.Results {
		results = append(results, IDeviceRelationship(&res.DefaultDeviceRelationship))
	}
	return results, &resp.DeviceRelationships.Pagination.DefaultPagination, nil
}

// Assure that a device group exists.
func AssureDeviceGroup(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupCreateRequest,
) (IDeviceGroup, bool, error) {
	gresp, err := GetDeviceGroupsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceGroup(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device group.
func CreateDeviceGroup(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupCreateRequest,
) (IDeviceGroup, error) {
	cresp, err := createDeviceGroup(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceGroup, nil
}

// Get device groups by token.
func GetDeviceGroupsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceGroup, error) {
	gresp, err := getDeviceGroupsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceGroup)
	if gresp != nil {
		for _, res := range gresp.DeviceGroupsByToken {
			itypes[res.Token] = IDeviceGroup(&res)
		}
	}
	return itypes, nil
}

// List device groups based on criteria.
func ListDeviceGroups(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IDeviceGroup, *DefaultPagination, error) {
	resp, err := listDeviceGroups(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceGroup, 0)
	for _, res := range resp.DeviceGroups.Results {
		results = append(results, IDeviceGroup(&res.DefaultDeviceGroup))
	}
	return results, &resp.DeviceGroups.Pagination.DefaultPagination, nil
}

// Assure that a device group relationship type exists.
func AssureDeviceGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupRelationshipTypeCreateRequest,
) (IDeviceGroupRelationshipType, bool, error) {
	gresp, err := GetDeviceGroupRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceGroupRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device group relationship type.
func CreateDeviceGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupRelationshipTypeCreateRequest,
) (IDeviceGroupRelationshipType, error) {
	cresp, err := createDeviceGroupRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceGroupRelationshipType, nil
}

// Get a device group relationship types by token.
func GetDeviceGroupRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceGroupRelationshipType, error) {
	gresp, err := getDeviceGroupRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceGroupRelationshipType)
	if gresp != nil {
		for _, res := range gresp.DeviceGroupRelationshipTypesByToken {
			itypes[res.Token] = IDeviceGroupRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List device group relationship types based on criteria.
func ListDeviceGroupRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IDeviceGroupRelationshipType, *DefaultPagination, error) {
	resp, err := listDeviceGroupRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceGroupRelationshipType, 0)
	for _, res := range resp.DeviceGroupRelationshipTypes.Results {
		results = append(results, IDeviceGroupRelationshipType(&res.DefaultDeviceGroupRelationshipType))
	}
	return results, &resp.DeviceGroupRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a device group relationship exists.
func AssureDeviceGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupRelationshipCreateRequest,
) (IDeviceGroupRelationship, bool, error) {
	gresp, err := GetDeviceGroupRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateDeviceGroupRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new device group relationship.
func CreateDeviceGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.DeviceGroupRelationshipCreateRequest,
) (IDeviceGroupRelationship, error) {
	cresp, err := createDeviceGroupRelationship(ctx, client, request.Token, request.SourceDeviceGroup,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateDeviceGroupRelationship, nil
}

// Get a device group relationships by token.
func GetDeviceGroupRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IDeviceGroupRelationship, error) {
	gresp, err := getDeviceGroupRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IDeviceGroupRelationship)
	if gresp != nil {
		for _, res := range gresp.DeviceGroupRelationshipsByToken {
			itypes[res.Token] = IDeviceGroupRelationship(&res)
		}
	}
	return itypes, nil
}

// List device group relationships based on criteria.
func ListDeviceGroupRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceDeviceGroup *string,
	relationshipType *string,
) ([]IDeviceGroupRelationship, *DefaultPagination, error) {
	resp, err := listDeviceGroupRelationships(ctx, client, pageNumber, pageSize, sourceDeviceGroup, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IDeviceGroupRelationship, 0)
	for _, res := range resp.DeviceGroupRelationships.Results {
		results = append(results, IDeviceGroupRelationship(&res.DefaultDeviceGroupRelationship))
	}
	return results, &resp.DeviceGroupRelationships.Pagination.DefaultPagination, nil
}
