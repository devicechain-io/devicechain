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

// Assure that a asset relationship type exists.
func AssureAssetRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetRelationshipTypeCreateRequest,
) (IAssetRelationshipType, bool, error) {
	gresp, err := GetAssetRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset relationship type.
func CreateAssetRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetRelationshipTypeCreateRequest,
) (IAssetRelationshipType, error) {
	cresp, err := createAssetRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetRelationshipType, nil
}

// Get asset relationship types by token.
func GetAssetRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetRelationshipType, error) {
	gresp, err := getAssetRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetRelationshipType)
	if gresp != nil {
		for _, res := range gresp.AssetRelationshipTypesByToken {
			itypes[res.Token] = IAssetRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List asset relationship types based on criteria.
func ListAssetRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAssetRelationshipType, *DefaultPagination, error) {
	resp, err := listAssetRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetRelationshipType, 0)
	for _, res := range resp.AssetRelationshipTypes.Results {
		results = append(results, IAssetRelationshipType(&res.DefaultAssetRelationshipType))
	}
	return results, &resp.AssetRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a asset relationship exists.
func AssureAssetRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AssetRelationshipCreateRequest,
) (IAssetRelationship, bool, error) {
	gresp, err := GetAssetRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset relationship.
func CreateAssetRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AssetRelationshipCreateRequest,
) (IAssetRelationship, error) {
	cresp, err := createAssetRelationship(ctx, client, request.Token, request.SourceAsset,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetRelationship, nil
}

// Get asset relationships by token.
func GetAssetRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetRelationship, error) {
	gresp, err := getAssetRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetRelationship)
	if gresp != nil {
		for _, res := range gresp.AssetRelationshipsByToken {
			itypes[res.Token] = IAssetRelationship(&res)
		}
	}
	return itypes, nil
}

// List asset relationships based on criteria.
func ListAssetRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceAsset *string,
	relationshipType *string,
) ([]IAssetRelationship, *DefaultPagination, error) {
	resp, err := listAssetRelationships(ctx, client, pageNumber, pageSize, sourceAsset, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetRelationship, 0)
	for _, res := range resp.AssetRelationships.Results {
		results = append(results, IAssetRelationship(&res.DefaultAssetRelationship))
	}
	return results, &resp.AssetRelationships.Pagination.DefaultPagination, nil
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

// Assure that a asset group relationship type exists.
func AssureAssetGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupRelationshipTypeCreateRequest,
) (IAssetGroupRelationshipType, bool, error) {
	gresp, err := GetAssetGroupRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetGroupRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset group relationship type.
func CreateAssetGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupRelationshipTypeCreateRequest,
) (IAssetGroupRelationshipType, error) {
	cresp, err := createAssetGroupRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetGroupRelationshipType, nil
}

// Get a asset group relationship types by token.
func GetAssetGroupRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetGroupRelationshipType, error) {
	gresp, err := getAssetGroupRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetGroupRelationshipType)
	if gresp != nil {
		for _, res := range gresp.AssetGroupRelationshipTypesByToken {
			itypes[res.Token] = IAssetGroupRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List asset group relationship types based on criteria.
func ListAssetGroupRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAssetGroupRelationshipType, *DefaultPagination, error) {
	resp, err := listAssetGroupRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetGroupRelationshipType, 0)
	for _, res := range resp.AssetGroupRelationshipTypes.Results {
		results = append(results, IAssetGroupRelationshipType(&res.DefaultAssetGroupRelationshipType))
	}
	return results, &resp.AssetGroupRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a asset group relationship exists.
func AssureAssetGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupRelationshipCreateRequest,
) (IAssetGroupRelationship, bool, error) {
	gresp, err := GetAssetGroupRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAssetGroupRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new asset group relationship.
func CreateAssetGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AssetGroupRelationshipCreateRequest,
) (IAssetGroupRelationship, error) {
	cresp, err := createAssetGroupRelationship(ctx, client, request.Token, request.SourceAssetGroup,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAssetGroupRelationship, nil
}

// Get a asset group relationships by token.
func GetAssetGroupRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAssetGroupRelationship, error) {
	gresp, err := getAssetGroupRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAssetGroupRelationship)
	if gresp != nil {
		for _, res := range gresp.AssetGroupRelationshipsByToken {
			itypes[res.Token] = IAssetGroupRelationship(&res)
		}
	}
	return itypes, nil
}

// List asset group relationships based on criteria.
func ListAssetGroupRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceAssetGroup *string,
	relationshipType *string,
) ([]IAssetGroupRelationship, *DefaultPagination, error) {
	resp, err := listAssetGroupRelationships(ctx, client, pageNumber, pageSize, sourceAssetGroup, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAssetGroupRelationship, 0)
	for _, res := range resp.AssetGroupRelationships.Results {
		results = append(results, IAssetGroupRelationship(&res.DefaultAssetGroupRelationship))
	}
	return results, &resp.AssetGroupRelationships.Pagination.DefaultPagination, nil
}
