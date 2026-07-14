// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

import (
	"context"

	"github.com/Khan/genqlient/graphql"
	"github.com/devicechain-io/dc-device-management/model"
)

// Assure that an entity group exists (ADR-061).
func AssureEntityGroup(
	ctx context.Context,
	client graphql.Client,
	request model.EntityGroupCreateRequest,
) (IEntityGroup, bool, error) {
	gresp, err := GetEntityGroupsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateEntityGroup(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new entity group.
func CreateEntityGroup(
	ctx context.Context,
	client graphql.Client,
	request model.EntityGroupCreateRequest,
) (IEntityGroup, error) {
	cresp, err := createEntityGroup(ctx, client, request.Token, request.MemberType,
		request.MembershipMode, request.Name, request.Description, request.ImageUrl,
		request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateEntityGroup, nil
}

// Get entity groups by token.
func GetEntityGroupsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]IEntityGroup, error) {
	gresp, err := getEntityGroupsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]IEntityGroup)
	if gresp != nil {
		for _, res := range gresp.EntityGroupsByToken {
			itypes[res.Token] = IEntityGroup(&res)
		}
	}
	return itypes, nil
}

// List entity groups based on criteria.
func ListEntityGroups(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]IEntityGroup, *DefaultPagination, error) {
	resp, err := listEntityGroups(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]IEntityGroup, 0)
	for _, res := range resp.EntityGroups.Results {
		results = append(results, IEntityGroup(&res.DefaultEntityGroup))
	}
	return results, &resp.EntityGroups.Pagination.DefaultPagination, nil
}
