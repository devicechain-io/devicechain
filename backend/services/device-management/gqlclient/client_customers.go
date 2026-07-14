// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
