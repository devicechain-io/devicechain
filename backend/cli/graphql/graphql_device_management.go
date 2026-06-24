/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

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

// Assure a device relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceRelationshipType(ctx context.Context, token string, name *string,
	description *string, metadata *string, tracked bool) {
	assure("device relationship type", token)
	req := dmmodel.DeviceRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
		Tracked:     tracked,
	}
	resp, wascreated, err := dmgql.AssureDeviceRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device relationship types by token.
func (dmc *DeviceManagementClient) GetDeviceRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IDeviceRelationshipType, error) {
	return dmgql.GetDeviceRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List device relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a device relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceRelationship(ctx context.Context, token string, source string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("device relationship", token)
	req := dmmodel.DeviceRelationshipCreateRequest{
		Token:            token,
		SourceDevice:     source,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureDeviceRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device relationships by token.
func (dmc *DeviceManagementClient) GetDeviceRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IDeviceRelationship, error) {
	return dmgql.GetDeviceRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List device relationships that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure a device group relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceGroupRelationshipType(ctx context.Context,
	token string, name *string, description *string, metadata *string) {
	assure("device group relationship type", token)
	req := dmmodel.DeviceGroupRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureDeviceGroupRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device group relationship types by token.
func (dmc *DeviceManagementClient) GetDeviceGroupRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IDeviceGroupRelationshipType, error) {
	return dmgql.GetDeviceGroupRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List device group relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceGroupRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceGroupRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceGroupRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a device group relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureDeviceGroupRelationship(ctx context.Context,
	token string, deviceGroup string, targets dmmodel.EntityRelationshipCreateRequest,
	relation string, metadata *string) {
	assure("device group relationship", token)
	req := dmmodel.DeviceGroupRelationshipCreateRequest{
		Token:             token,
		SourceDeviceGroup: deviceGroup,
		Targets:           targets,
		RelationshipType:  relation,
		Metadata:          metadata,
	}
	resp, wascreated, err := dmgql.AssureDeviceGroupRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get device group relationships by token.
func (dmc *DeviceManagementClient) GetDeviceGroupRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IDeviceGroupRelationship, error) {
	return dmgql.GetDeviceGroupRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List device group relationships that meet criteria.
func (dmc *DeviceManagementClient) ListDeviceGroupRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IDeviceGroupRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListDeviceGroupRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure an asset relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetRelationshipType(ctx context.Context, token string, name *string,
	description *string, metadata *string) {
	assure("asset relationship type", token)
	req := dmmodel.AssetRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset relationship types by token.
func (dmc *DeviceManagementClient) GetAssetRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAssetRelationshipType, error) {
	return dmgql.GetAssetRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List asset relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListAssetRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an asset relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetRelationship(ctx context.Context, token string, source string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("asset relationship", token)
	req := dmmodel.AssetRelationshipCreateRequest{
		Token:            token,
		SourceAsset:      source,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset relationships by token.
func (dmc *DeviceManagementClient) GetAssetRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAssetRelationship, error) {
	return dmgql.GetAssetRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List asset relationships that meet criteria.
func (dmc *DeviceManagementClient) ListAssetRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure an asset group relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetGroupRelationshipType(ctx context.Context, token string, name *string,
	description *string, metadata *string) {
	assure("asset group relationship type", token)
	req := dmmodel.AssetGroupRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetGroupRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset group relationship types by token.
func (dmc *DeviceManagementClient) GetAssetGroupRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAssetGroupRelationshipType, error) {
	return dmgql.GetAssetGroupRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List asset group relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListAssetGroupRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetGroupRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetGroupRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an asset group relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAssetGroupRelationship(ctx context.Context, token string, assetGroup string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("asset group relationship", token)
	req := dmmodel.AssetGroupRelationshipCreateRequest{
		Token:            token,
		SourceAssetGroup: assetGroup,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureAssetGroupRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get asset group relationships by token.
func (dmc *DeviceManagementClient) GetAssetGroupRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAssetGroupRelationship, error) {
	return dmgql.GetAssetGroupRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List asset group relationships that meet criteria.
func (dmc *DeviceManagementClient) ListAssetGroupRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAssetGroupRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListAssetGroupRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure an area relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaRelationshipType(ctx context.Context, token string, name *string,
	description *string, metadata *string) {
	assure("area relationship type", token)
	req := dmmodel.AreaRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area relationship types by token.
func (dmc *DeviceManagementClient) GetAreaRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAreaRelationshipType, error) {
	return dmgql.GetAreaRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List area relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListAreaRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an area relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaRelationship(ctx context.Context, token string, source string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("area relationship", token)
	req := dmmodel.AreaRelationshipCreateRequest{
		Token:            token,
		SourceArea:       source,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area relationships by token.
func (dmc *DeviceManagementClient) GetAreaRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAreaRelationship, error) {
	return dmgql.GetAreaRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List area relationships that meet criteria.
func (dmc *DeviceManagementClient) ListAreaRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure an area group relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaGroupRelationshipType(ctx context.Context, token string, name *string,
	description *string, metadata *string) {
	assure("area group relationship type", token)
	req := dmmodel.AreaGroupRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaGroupRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area group relationship types by token.
func (dmc *DeviceManagementClient) GetAreaGroupRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAreaGroupRelationshipType, error) {
	return dmgql.GetAreaGroupRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List area group relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListAreaGroupRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaGroupRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaGroupRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure an area group relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureAreaGroupRelationship(ctx context.Context, token string, areaGroup string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("area group relationship", token)
	req := dmmodel.AreaGroupRelationshipCreateRequest{
		Token:            token,
		SourceAreaGroup:  areaGroup,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureAreaGroupRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get area group relationships by token.
func (dmc *DeviceManagementClient) GetAreaGroupRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.IAreaGroupRelationship, error) {
	return dmgql.GetAreaGroupRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List area group relationships that meet criteria.
func (dmc *DeviceManagementClient) ListAreaGroupRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.IAreaGroupRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListAreaGroupRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure a customer relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerRelationshipType(ctx context.Context, token string,
	name *string, description *string, metadata *string) {
	assure("customer relationship type", token)
	req := dmmodel.CustomerRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer relationship types by token.
func (dmc *DeviceManagementClient) GetCustomerRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerRelationshipType, error) {
	return dmgql.GetCustomerRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List customer relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a customer relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerRelationship(ctx context.Context, token string, source string,
	targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("customer relationship", token)
	req := dmmodel.CustomerRelationshipCreateRequest{
		Token:            token,
		SourceCustomer:   source,
		Targets:          targets,
		RelationshipType: relation,
		Metadata:         metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer relationships by token.
func (dmc *DeviceManagementClient) GetCustomerRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerRelationship, error) {
	return dmgql.GetCustomerRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List customer relationships that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerRelationships(ctx, dmc.Client, pageNumber, pageSize)
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

// Assure a customer group relationship type (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerGroupRelationshipType(ctx context.Context, token string,
	name *string, description *string, metadata *string) {
	assure("customer group relationship type", token)
	req := dmmodel.CustomerGroupRelationshipTypeCreateRequest{
		Token:       token,
		Name:        name,
		Description: description,
		Metadata:    metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerGroupRelationshipType(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer group relationship types by token.
func (dmc *DeviceManagementClient) GetCustomerGroupRelationshipTypesByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerGroupRelationshipType, error) {
	return dmgql.GetCustomerGroupRelationshipTypesByToken(ctx, dmc.Client, tokens)
}

// List customer group relationship types that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerGroupRelationshipTypes(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerGroupRelationshipType, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerGroupRelationshipTypes(ctx, dmc.Client, pageNumber, pageSize)
}

// Assure a customer group relationship (check for existing or create new).
func (dmc *DeviceManagementClient) AssureCustomerGroupRelationship(ctx context.Context, token string,
	customerGroup string, targets dmmodel.EntityRelationshipCreateRequest, relation string, metadata *string) {
	assure("customer group relationship", token)
	req := dmmodel.CustomerGroupRelationshipCreateRequest{
		Token:               token,
		SourceCustomerGroup: customerGroup,
		Targets:             targets,
		RelationshipType:    relation,
		Metadata:            metadata,
	}
	resp, wascreated, err := dmgql.AssureCustomerGroupRelationship(ctx, dmc.Client, req)
	if err != nil {
		panic(err)
	}
	if wascreated {
		created(resp.GetToken())
	} else {
		found(resp.GetToken())
	}
}

// Get customer group relationships by token.
func (dmc *DeviceManagementClient) GetCustomerGroupRelationshipsByToken(ctx context.Context,
	tokens []string) (map[string]dmgql.ICustomerGroupRelationship, error) {
	return dmgql.GetCustomerGroupRelationshipsByToken(ctx, dmc.Client, tokens)
}

// List customer group relationships that meet criteria.
func (dmc *DeviceManagementClient) ListCustomerGroupRelationships(ctx context.Context,
	pageNumber int, pageSize int) ([]dmgql.ICustomerGroupRelationship, *dmgql.DefaultPagination, error) {
	return dmgql.ListCustomerGroupRelationships(ctx, dmc.Client, pageNumber, pageSize)
}
