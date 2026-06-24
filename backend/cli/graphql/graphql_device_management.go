// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/Khan/genqlient/graphql"
	dmgql "github.com/devicechain-io/dc-device-management/gqlclient"
	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/spf13/cobra"
)

type DeviceManagementClient struct {
	graphql.Client
}

// Creates a device management GraphQL client based on command flags and other settings.
func NewDeviceManagementGraphQLClient(cmd *cobra.Command) DeviceManagementClient {
	cli := GetGraphQLClientForCommand(cmd, "device-management")
	dmclient := DeviceManagementClient{
		Client: cli,
	}
	return dmclient
}

// Assure a device type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceType(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("device type", token)
	req := dmmodel.DeviceTypeCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureDeviceType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device types by token.
func (dmc *DeviceManagementClient) GetDeviceTypesByToken(ctx context.Context, tokens []string) (map[string]dmgql.IDeviceType, error) {
	return dmgql.GetDeviceTypesByToken(ctx, dmc.Client, tokens)
}

// List device types that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceType, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a device (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDevice(ctx context.Context, token string, deviceTypeToken string, name *string,
	description *string, metadata *string) {
	assure("device", token)
	req := dmmodel.DeviceCreateRequest{
		Token:           token,
		DeviceTypeToken: deviceTypeToken,
		Name:            name,
		Description:     description,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureDevice(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get devices by token.
func (dmc *DeviceManagementClient) GetDevicesByToken(ctx context.Context, tokens []string) (map[string]dmgql.IDevice, error) {
	return dmgql.GetDevicesByToken(ctx, dmc.Client, tokens)
}

// List devices that meet criteria.
func (dmc *DeviceManagementClient) ListDevices(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDevice, *dmgql.DefaultPagination, error) {
	return dmgql.ListDevices(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a device group (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceGroup(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("device group", token)
	req := dmmodel.DeviceGroupCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureDeviceGroup(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device groups by token.
func (dmc *DeviceManagementClient) GetDeviceGroupsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IDeviceGroup, error) {
	return dmgql.GetDeviceGroupsByToken(ctx, dmc.Client, tokens)
}

// List device groups that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceGroups(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceGroup, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceGroups(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an asset type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetType(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("asset type", token)
	req := dmmodel.AssetTypeCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset types by token.
func (dmc *DeviceManagementClient) GetAssetTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAssetType, error) {
	return dmgql.GetAssetTypesByToken(ctx, dmc.Client, tokens)
}

// List asset types that meet criteria.
func (dmc *DeviceManagementClient) ListAssetTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an asset (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAsset(ctx context.Context, token string, assetTypeToken string, name *string,
	description *string, metadata *string) {
	assure("asset", token)
	req := dmmodel.AssetCreateRequest{
		Token:          token,
		AssetTypeToken: assetTypeToken,
		Name:           name,
		Description:    description,
		Metadata:       metadata,
	}
	resp, wascreated, err := dmgql.AssureAsset(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get assets by token.
func (dmc *DeviceManagementClient) GetAssetsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAsset, error) {
	return dmgql.GetAssetsByToken(ctx, dmc.Client, tokens)
}

// List assets that meet criteria.
func (dmc *DeviceManagementClient) ListAssets(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAsset, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssets(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an asset group (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetGroup(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("asset group", token)
	req := dmmodel.AssetGroupCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetGroup(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset groups by token.
func (dmc *DeviceManagementClient) GetAssetGroupsByToken(ctx context.Context, tokens []string) (map[string]dmgql.IAssetGroup, error) {
	return dmgql.GetAssetGroupsByToken(ctx, dmc.Client, tokens)
}

// List asset groups that meet criteria.
func (dmc *DeviceManagementClient) ListAssetGroups(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetGroup, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetGroups(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an area type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaType(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("area type", token)
	req := dmmodel.AreaTypeCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area types by token.
func (dmc *DeviceManagementClient) GetAreaTypesByToken(ctx context.Context, tokens []string) (map[string]dmgql.IAreaType, error) {
	return dmgql.GetAreaTypesByToken(ctx, dmc.Client, tokens)
}

// List area types that meet criteria.
func (dmc *DeviceManagementClient) ListAreaTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an area (check for existing or create new).
func (dmc *DeviceManagementClient) AssureArea(ctx context.Context, token string, areaTypeToken string, name *string,
	description *string, metadata *string) {
	assure("area", token)
	req := dmmodel.AreaCreateRequest{
		Token:         token,
		AreaTypeToken: areaTypeToken,
		Name:          name,
		Description:   description,
		Metadata:      metadata,
	}
	resp, wascreated, err := dmgql.AssureArea(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get areas by token.
func (dmc *DeviceManagementClient) GetAreasByToken(ctx context.Context, tokens []string) (map[string]dmgql.IArea, error) {
	return dmgql.GetAreasByToken(ctx, dmc.Client, tokens)
}

// List areas that meet criteria.
func (dmc *DeviceManagementClient) ListAreas(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IArea, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreas(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an area group (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaGroup(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("area group", token)
	req := dmmodel.AreaGroupCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaGroup(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area groups by token.
func (dmc *DeviceManagementClient) GetAreaGroupsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAreaGroup, error) {
	return dmgql.GetAreaGroupsByToken(ctx, dmc.Client, tokens)
}

// List area groups that meet criteria.
func (dmc *DeviceManagementClient) ListAreaGroups(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaGroup, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaGroups(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a customer type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerType(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("customer type", token)
	req := dmmodel.CustomerTypeCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer types by token.
func (dmc *DeviceManagementClient) GetCustomerTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerType, error) {
	return dmgql.GetCustomerTypesByToken(ctx, dmc.Client, tokens)
}

// List customer types that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerType, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a customer (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomer(ctx context.Context, token string, customerTypeToken string,
	name *string, description *string, metadata *string) {
	assure("customer", token)
	req := dmmodel.CustomerCreateRequest{
		Token:             token,
		CustomerTypeToken: customerTypeToken,
		Name:              name,
		Description:       description,
		Metadata:          metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomer(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customers by token.
func (dmc *DeviceManagementClient) GetCustomersByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomer, error) {
	return dmgql.GetCustomersByToken(ctx, dmc.Client, tokens)
}

// List customers that meet criteria.
func (dmc *DeviceManagementClient) ListCustomers(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomer, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomers(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a customer group (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerGroup(ctx context.Context, token string, name *string,
	description *string, imageUrl *string, icon *string, backgroundColor *string, foregroundColor *string,
	borderColor *string, metadata *string) {
	assure("customer group", token)
	req := dmmodel.CustomerGroupCreateRequest{
		Token:           token,
		Name:            name,
		Description:     description,
		ImageUrl:        imageUrl,
		Icon:            icon,
		BackgroundColor: backgroundColor,
		ForegroundColor: foregroundColor,
		BorderColor:     borderColor,
		Metadata:        metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerGroup(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer groups by token.
func (dmc *DeviceManagementClient) GetCustomerGroupsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerGroup, error) {
	return dmgql.GetCustomerGroupsByToken(ctx, dmc.Client, tokens)
}

// List customer groups that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerGroups(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerGroup, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerGroups(ctx, dmc.Client, pageNumber, pageSize)
}
