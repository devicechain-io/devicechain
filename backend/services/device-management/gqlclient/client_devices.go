// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
