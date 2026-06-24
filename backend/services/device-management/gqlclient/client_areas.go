// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
