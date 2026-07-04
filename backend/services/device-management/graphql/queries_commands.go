// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find command definitions by unique id.
func (r *SchemaResolver) CommandDefinitionsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CommandDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CommandDefinitionsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CommandDefinitionResolver, 0)
	for _, cd := range found {
		result = append(result, &CommandDefinitionResolver{
			M: *cd,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// Find command definitions by unique token.
func (r *SchemaResolver) CommandDefinitionsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CommandDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CommandDefinitionsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CommandDefinitionResolver, 0)
	for _, cd := range found {
		result = append(result, &CommandDefinitionResolver{
			M: *cd,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// List all command definitions that match the given criteria.
func (r *SchemaResolver) CommandDefinitions(ctx context.Context, args struct {
	Criteria model.CommandDefinitionSearchCriteria
}) (*CommandDefinitionSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CommandDefinitions(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &CommandDefinitionSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
