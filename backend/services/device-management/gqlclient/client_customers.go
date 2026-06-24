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

// Assure that a customer type exists.
func AssureCustomerType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerTypeCreateRequest,
) (ICustomerType, bool, error) {
	gresp, err := GetCustomerTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer type.
func CreateCustomerType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerTypeCreateRequest,
) (ICustomerType, error) {
	cresp, err := createCustomerType(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerType, nil
}

// Get customer types by token.
func GetCustomerTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerType, error) {
	gresp, err := getCustomerTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerType)
	if gresp != nil {
		for _, res := range gresp.CustomerTypesByToken {
			itypes[res.Token] = ICustomerType(&res)
		}
	}
	return itypes, nil
}

// List customer types based on criteria.
func ListCustomerTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]ICustomerType, *DefaultPagination, error) {
	resp, err := listCustomerTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerType, 0)
	for _, res := range resp.CustomerTypes.Results {
		results = append(results, ICustomerType(&res.DefaultCustomerType))
	}
	return results, &resp.CustomerTypes.Pagination.DefaultPagination, nil
}

// Assure that a customer exists.
func AssureCustomer(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerCreateRequest,
) (ICustomer, bool, error) {
	gresp, err := GetCustomersByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomer(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer.
func CreateCustomer(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerCreateRequest,
) (ICustomer, error) {
	cresp, err := createCustomer(ctx, client, request.Token, request.CustomerTypeToken,
		request.Name, request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomer, nil
}

// Get customers by token.
func GetCustomersByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomer, error) {
	gresp, err := getCustomersByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomer)
	if gresp != nil {
		for _, res := range gresp.CustomersByToken {
			itypes[res.Token] = ICustomer(&res)
		}
	}
	return itypes, nil
}

// List customers based on criteria.
func ListCustomers(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]ICustomer, *DefaultPagination, error) {
	resp, err := listCustomers(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomer, 0)
	for _, res := range resp.Customers.Results {
		results = append(results, ICustomer(&res.DefaultCustomer))
	}
	return results, &resp.Customers.Pagination.DefaultPagination, nil
}

// Assure that a customer relationship type exists.
func AssureCustomerRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerRelationshipTypeCreateRequest,
) (ICustomerRelationshipType, bool, error) {
	gresp, err := GetCustomerRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer relationship type.
func CreateCustomerRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerRelationshipTypeCreateRequest,
) (ICustomerRelationshipType, error) {
	cresp, err := createCustomerRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerRelationshipType, nil
}

// Get customer relationship types by token.
func GetCustomerRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerRelationshipType, error) {
	gresp, err := getCustomerRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerRelationshipType)
	if gresp != nil {
		for _, res := range gresp.CustomerRelationshipTypesByToken {
			itypes[res.Token] = ICustomerRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List customer relationship types based on criteria.
func ListCustomerRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]ICustomerRelationshipType, *DefaultPagination, error) {
	resp, err := listCustomerRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerRelationshipType, 0)
	for _, res := range resp.CustomerRelationshipTypes.Results {
		results = append(results, ICustomerRelationshipType(&res.DefaultCustomerRelationshipType))
	}
	return results, &resp.CustomerRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a customer relationship exists.
func AssureCustomerRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerRelationshipCreateRequest,
) (ICustomerRelationship, bool, error) {
	gresp, err := GetCustomerRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer relationship.
func CreateCustomerRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerRelationshipCreateRequest,
) (ICustomerRelationship, error) {
	cresp, err := createCustomerRelationship(ctx, client, request.Token, request.SourceCustomer,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerRelationship, nil
}

// Get customer relationships by token.
func GetCustomerRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerRelationship, error) {
	gresp, err := getCustomerRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerRelationship)
	if gresp != nil {
		for _, res := range gresp.CustomerRelationshipsByToken {
			itypes[res.Token] = ICustomerRelationship(&res)
		}
	}
	return itypes, nil
}

// List customer relationships based on criteria.
func ListCustomerRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceCustomer *string,
	relationshipType *string,
) ([]ICustomerRelationship, *DefaultPagination, error) {
	resp, err := listCustomerRelationships(ctx, client, pageNumber, pageSize, sourceCustomer, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerRelationship, 0)
	for _, res := range resp.CustomerRelationships.Results {
		results = append(results, ICustomerRelationship(&res.DefaultCustomerRelationship))
	}
	return results, &resp.CustomerRelationships.Pagination.DefaultPagination, nil
}

// Assure that a customer group exists.
func AssureCustomerGroup(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupCreateRequest,
) (ICustomerGroup, bool, error) {
	gresp, err := GetCustomerGroupsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerGroup(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer group.
func CreateCustomerGroup(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupCreateRequest,
) (ICustomerGroup, error) {
	cresp, err := createCustomerGroup(ctx, client, request.Token, request.Name, request.Description,
		request.ImageUrl, request.Icon, request.BackgroundColor, request.ForegroundColor,
		request.BorderColor, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerGroup, nil
}

// Get customer groups by token.
func GetCustomerGroupsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerGroup, error) {
	gresp, err := getCustomerGroupsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerGroup)
	if gresp != nil {
		for _, res := range gresp.CustomerGroupsByToken {
			itypes[res.Token] = ICustomerGroup(&res)
		}
	}
	return itypes, nil
}

// List customer groups based on criteria.
func ListCustomerGroups(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]ICustomerGroup, *DefaultPagination, error) {
	resp, err := listCustomerGroups(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerGroup, 0)
	for _, res := range resp.CustomerGroups.Results {
		results = append(results, ICustomerGroup(&res.DefaultCustomerGroup))
	}
	return results, &resp.CustomerGroups.Pagination.DefaultPagination, nil
}

// Assure that a customer group relationship type exists.
func AssureCustomerGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupRelationshipTypeCreateRequest,
) (ICustomerGroupRelationshipType, bool, error) {
	gresp, err := GetCustomerGroupRelationshipTypesByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerGroupRelationshipType(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer group relationship type.
func CreateCustomerGroupRelationshipType(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupRelationshipTypeCreateRequest,
) (ICustomerGroupRelationshipType, error) {
	cresp, err := createCustomerGroupRelationshipType(ctx, client, request.Token, request.Name,
		request.Description, request.Metadata)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerGroupRelationshipType, nil
}

// Get a customer group relationship types by token.
func GetCustomerGroupRelationshipTypesByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerGroupRelationshipType, error) {
	gresp, err := getCustomerGroupRelationshipTypesByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerGroupRelationshipType)
	if gresp != nil {
		for _, res := range gresp.CustomerGroupRelationshipTypesByToken {
			itypes[res.Token] = ICustomerGroupRelationshipType(&res)
		}
	}
	return itypes, nil
}

// List customer group relationship types based on criteria.
func ListCustomerGroupRelationshipTypes(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
) ([]ICustomerGroupRelationshipType, *DefaultPagination, error) {
	resp, err := listCustomerGroupRelationshipTypes(ctx, client, pageNumber, pageSize)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerGroupRelationshipType, 0)
	for _, res := range resp.CustomerGroupRelationshipTypes.Results {
		results = append(results, ICustomerGroupRelationshipType(&res.DefaultCustomerGroupRelationshipType))
	}
	return results, &resp.CustomerGroupRelationshipTypes.Pagination.DefaultPagination, nil
}

// Assure that a customer group relationship exists.
func AssureCustomerGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupRelationshipCreateRequest,
) (ICustomerGroupRelationship, bool, error) {
	gresp, err := GetCustomerGroupRelationshipsByToken(ctx, client, []string{request.Token})
	if err != nil {
		return nil, false, err
	}
	if gresp[request.Token] != nil {
		return gresp[request.Token], false, nil
	}
	cresp, err := CreateCustomerGroupRelationship(ctx, client, request)
	if err != nil {
		return nil, false, err
	}
	return cresp, true, nil
}

// Create a new customer group relationship.
func CreateCustomerGroupRelationship(
	ctx context.Context,
	client graphql.Client,
	request model.CustomerGroupRelationshipCreateRequest,
) (ICustomerGroupRelationship, error) {
	cresp, err := createCustomerGroupRelationship(ctx, client, request.Token, request.SourceCustomerGroup,
		targets(request.Targets), request.RelationshipType)
	if err != nil {
		return nil, err
	}
	return &cresp.CreateCustomerGroupRelationship, nil
}

// Get a customer group relationships by token.
func GetCustomerGroupRelationshipsByToken(
	ctx context.Context,
	client graphql.Client,
	tokens []string,
) (map[string]ICustomerGroupRelationship, error) {
	gresp, err := getCustomerGroupRelationshipsByToken(ctx, client, tokens)
	if err != nil {
		return nil, err
	}
	itypes := make(map[string]ICustomerGroupRelationship)
	if gresp != nil {
		for _, res := range gresp.CustomerGroupRelationshipsByToken {
			itypes[res.Token] = ICustomerGroupRelationship(&res)
		}
	}
	return itypes, nil
}

// List customer group relationships based on criteria.
func ListCustomerGroupRelationships(
	ctx context.Context,
	client graphql.Client,
	pageNumber int,
	pageSize int,
	sourceCustomerGroup *string,
	relationshipType *string,
) ([]ICustomerGroupRelationship, *DefaultPagination, error) {
	resp, err := listCustomerGroupRelationships(ctx, client, pageNumber, pageSize, sourceCustomerGroup, relationshipType)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ICustomerGroupRelationship, 0)
	for _, res := range resp.CustomerGroupRelationships.Results {
		results = append(results, ICustomerGroupRelationship(&res.DefaultCustomerGroupRelationship))
	}
	return results, &resp.CustomerGroupRelationships.Pagination.DefaultPagination, nil
}
