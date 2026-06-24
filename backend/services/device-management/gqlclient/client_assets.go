// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

import (
	"context"

	"github.com/Khan/genqlient/graphql"
	"github.com/devicechain-io/dc-device-management/model"
)

// Assure that a asset type exists.
func AssureAssetType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetTypeCreateRequest,
) (IAssetType, bool, error) {
	gresp, err := GetAssetTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset type.
func CreateAssetType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetTypeCreateRequest,
) (IAssetType, error) {
	cresp, err := createAssetType(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetType, nil
}

// Get asset types by token.
func GetAssetTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetType, error) {
	gresp, err := getAssetTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetType)
	if gresp != nil {
		for _, res := range gresp.AssetTypesByToken {
			itypes[res.Token] = IAssetType(&res)
		}
	}
	return itypes, nil
}

// List asset types based on criteria.
func ListAssetTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAssetType, *DefaultPagination, error) {
	resp, err := listAssetTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetType, 0)
	for _, res := range resp.AssetTypes.Results {
		results = append(results, IAssetType(&res.DefaultAssetType))
	}
	return results, &resp.AssetTypes.Pagination.DefaultPagination, nil
}

// Assure that a asset exists.
func AssureAsset(
	ctx context.Context,
	client graphql.Client,
	request model.AssetCreateRequest,
) (IAsset, bool, error) {
	gresp, err := GetAssetsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAsset(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset.
func CreateAsset(
	ctx context.Context,
	client graphql.Client,
	request model.AssetCreateRequest,
) (IAsset, error) {
	cresp, err := createAsset(ctx, client, request.Token, request.AssetTypeToken,
		request.Name, request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAsset, nil
}

// Get assets by token.
func GetAssetsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAsset, error) {
	gresp, err := getAssetsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAsset)
	if gresp != nil {
		for _, res := range gresp.AssetsByToken {
			itypes[res.Token] = IAsset(&res)
		}
	}
	return itypes, nil
}

// List assets based on criteria.
func ListAssets(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAsset, *DefaultPagination, error) {
	resp, err := listAssets(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAsset, 0)
	for _, res := range resp.Assets.Results {
		results = append(results, IAsset(&res.DefaultAsset))
	}
	return results, &resp.Assets.Pagination.DefaultPagination, nil
}

// Assure that a asset group exists.
func AssureAssetGroup(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupCreateRequest,
) (IAssetGroup, bool, error) {
	gresp, err := GetAssetGroupsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetGroup(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset group.
func CreateAssetGroup(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupCreateRequest,
) (IAssetGroup, error) {
	cresp, err := createAssetGroup(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetGroup, nil
}

// Get asset groups by token.
func GetAssetGroupsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetGroup, error) {
	gresp, err := getAssetGroupsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetGroup)
	if gresp != nil {
		for _, res := range gresp.AssetGroupsByToken {
			itypes[res.Token] = IAssetGroup(&res)
		}
	}
	return itypes, nil
}

// List asset groups based on criteria.
func ListAssetGroups(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAssetGroup, *DefaultPagination, error) {
	resp, err := listAssetGroups(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetGroup, 0)
	for _, res := range resp.AssetGroups.Results {
		results = append(results, IAssetGroup(&res.DefaultAssetGroup))
	}
	return results, &resp.AssetGroups.Pagination.DefaultPagination, nil
}
