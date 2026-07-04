// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find alarm definitions by unique id.
func (r *SchemaResolver) AlarmDefinitionsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AlarmDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AlarmDefinitionsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AlarmDefinitionResolver, 0)
	for _, ad := range found {
		result = append(result, &AlarmDefinitionResolver{M: *ad, S: r, C: ctx})
	}
	return result, nil
}

// Find alarm definitions by unique token.
func (r *SchemaResolver) AlarmDefinitionsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AlarmDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AlarmDefinitionsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AlarmDefinitionResolver, 0)
	for _, ad := range found {
		result = append(result, &AlarmDefinitionResolver{M: *ad, S: r, C: ctx})
	}
	return result, nil
}

// List all alarm definitions that match the given criteria.
func (r *SchemaResolver) AlarmDefinitions(ctx context.Context, args struct {
	Criteria model.AlarmDefinitionSearchCriteria
}) (*AlarmDefinitionSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AlarmDefinitions(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AlarmDefinitionSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
