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

// Assure that a area type exists.
func AssureAreaType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaTypeCreateRequest,
) (IAreaType, bool, error) {
	gresp, err := GetAreaTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area type.
func CreateAreaType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaTypeCreateRequest,
) (IAreaType, error) {
	cresp, err := createAreaType(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaType, nil
}

// Get area types by token.
func GetAreaTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaType, error) {
	gresp, err := getAreaTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaType)
	if gresp != nil {
		for _, res := range gresp.AreaTypesByToken {
			itypes[res.Token] = IAreaType(&res)
		}
	}
	return itypes, nil
}

// List area types based on criteria.
func ListAreaTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAreaType, *DefaultPagination, error) {
	resp, err := listAreaTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaType, 0)
	for _, res := range resp.AreaTypes.Results {
		results = append(results, IAreaType(&res.DefaultAreaType))
	}
	return results, &resp.AreaTypes.Pagination.DefaultPagination, nil
}

// Assure that a area exists.
func AssureArea(
	ctx context.Context,
	client graphql.Client,
	request model.AreaCreateRequest,
) (IArea, bool, error) {
	gresp, err := GetAreasByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateArea(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area.
func CreateArea(
	ctx context.Context,
	client graphql.Client,
	request model.AreaCreateRequest,
) (IArea, error) {
	cresp, err := createArea(ctx, client, request.Token, request.AreaTypeToken,
		request.Name, request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateArea, nil
}

// Get areas by token.
func GetAreasByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IArea, error) {
	gresp, err := getAreasByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IArea)
	if gresp != nil {
		for _, res := range gresp.AreasByToken {
			itypes[res.Token] = IArea(&res)
		}
	}
	return itypes, nil
}

// List areas based on criteria.
func ListAreas(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IArea, *DefaultPagination, error) {
	resp, err := listAreas(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IArea, 0)
	for _, res := range resp.Areas.Results {
		results = append(results, IArea(&res.DefaultArea))
	}
	return results, &resp.Areas.Pagination.DefaultPagination, nil
}

// Assure that a area relationship type exists.
func AssureAreaRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaRelationshipTypeCreateRequest,
) (IAreaRelationshipType, bool, error) {
	gresp, err := GetAreaRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area relationship type.
func CreateAreaRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaRelationshipTypeCreateRequest,
) (IAreaRelationshipType, error) {
	cresp, err := createAreaRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaRelationshipType, nil
}

// Get area relationship types by token.
func GetAreaRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaRelationshipType, error) {
	gresp, err := getAreaRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaRelationshipType)
	if gresp != nil {
		for _, res := range gresp.AreaRelationshipTypesByToken {
			itypes[res.Token] = IAreaRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List area relationship types based on criteria.
func ListAreaRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAreaRelationshipType, *DefaultPagination, error) {
	resp, err := listAreaRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaRelationshipType, 0)
	for _, res := range resp.AreaRelationshipTypes.Results {
		results = append(results, IAreaRelationshipType(&res.DefaultAreaRelationshipType))
	}
	return results, &resp.AreaRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a area relationship exists.
func AssureAreaRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AreaRelationshipCreateRequest,
) (IAreaRelationship, bool, error) {
	gresp, err := GetAreaRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area relationship.
func CreateAreaRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AreaRelationshipCreateRequest,
) (IAreaRelationship, error) {
	cresp, err := createAreaRelationship(ctx, client, request.Token, request.SourceArea,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaRelationship, nil
}

// Get area relationships by token.
func GetAreaRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaRelationship, error) {
	gresp, err := getAreaRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaRelationship)
	if gresp != nil {
		for _, res := range gresp.AreaRelationshipsByToken {
			itypes[res.Token] = IAreaRelationship(&res)
		}
	}
	return itypes, nil
}

// List area relationships based on criteria.
func ListAreaRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceArea *string,
	relationshipType *string,
) ([]IAreaRelationship, *DefaultPagination, error) {
	resp, err := listAreaRelationships(ctx, client, pageNumber, pageSize, sourceArea, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaRelationship, 0)
	for _, res := range resp.AreaRelationships.Results {
		results = append(results, IAreaRelationship(&res.DefaultAreaRelationship))
	}
	return results, &resp.AreaRelationships.Pagination.DefaultPagination, nil
}

// Assure that a area group exists.
func AssureAreaGroup(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupCreateRequest,
) (IAreaGroup, bool, error) {
	gresp, err := GetAreaGroupsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaGroup(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area group.
func CreateAreaGroup(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupCreateRequest,
) (IAreaGroup, error) {
	cresp, err := createAreaGroup(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaGroup, nil
}

// Get area groups by token.
func GetAreaGroupsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaGroup, error) {
	gresp, err := getAreaGroupsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaGroup)
	if gresp != nil {
		for _, res := range gresp.AreaGroupsByToken {
			itypes[res.Token] = IAreaGroup(&res)
		}
	}
	return itypes, nil
}

// List area groups based on criteria.
func ListAreaGroups(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAreaGroup, *DefaultPagination, error) {
	resp, err := listAreaGroups(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaGroup, 0)
	for _, res := range resp.AreaGroups.Results {
		results = append(results, IAreaGroup(&res.DefaultAreaGroup))
	}
	return results, &resp.AreaGroups.Pagination.DefaultPagination, nil
}

// Assure that a area group relationship type exists.
func AssureAreaGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupRelationshipTypeCreateRequest,
) (IAreaGroupRelationshipType, bool, error) {
	gresp, err := GetAreaGroupRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaGroupRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area group relationship type.
func CreateAreaGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupRelationshipTypeCreateRequest,
) (IAreaGroupRelationshipType, error) {
	cresp, err := createAreaGroupRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaGroupRelationshipType, nil
}

// Get a area group relationship types by token.
func GetAreaGroupRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaGroupRelationshipType, error) {
	gresp, err := getAreaGroupRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaGroupRelationshipType)
	if gresp != nil {
		for _, res := range gresp.AreaGroupRelationshipTypesByToken {
			itypes[res.Token] = IAreaGroupRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List area group relationship types based on criteria.
func ListAreaGroupRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IAreaGroupRelationshipType, *DefaultPagination, error) {
	resp, err := listAreaGroupRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaGroupRelationshipType, 0)
	for _, res := range resp.AreaGroupRelationshipTypes.Results {
		results = append(results, IAreaGroupRelationshipType(&res.DefaultAreaGroupRelationshipType))
	}
	return results, &resp.AreaGroupRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a area group relationship exists.
func AssureAreaGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupRelationshipCreateRequest,
) (IAreaGroupRelationship, bool, error) {
	gresp, err := GetAreaGroupRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateAreaGroupRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new area group relationship.
func CreateAreaGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.AreaGroupRelationshipCreateRequest,
) (IAreaGroupRelationship, error) {
	cresp, err := createAreaGroupRelationship(ctx, client, request.Token, request.SourceAreaGroup,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateAreaGroupRelationship, nil
}

// Get a area group relationships by token.
func GetAreaGroupRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IAreaGroupRelationship, error) {
	gresp, err := getAreaGroupRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IAreaGroupRelationship)
	if gresp != nil {
		for _, res := range gresp.AreaGroupRelationshipsByToken {
			itypes[res.Token] = IAreaGroupRelationship(&res)
		}
	}
	return itypes, nil
}

// List area group relationships based on criteria.
func ListAreaGroupRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceAreaGroup *string,
	relationshipType *string,
) ([]IAreaGroupRelationship, *DefaultPagination, error) {
	resp, err := listAreaGroupRelationships(ctx, client, pageNumber, pageSize, sourceAreaGroup, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IAreaGroupRelationship, 0)
	for _, res := range resp.AreaGroupRelationships.Results {
		results = append(results, IAreaGroupRelationship(&res.DefaultAreaGroupRelationship))
	}
	return results, &resp.AreaGroupRelationships.Pagination.DefaultPagination, nil
}
