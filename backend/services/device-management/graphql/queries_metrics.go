// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find metric definitions by unique id.
func (r *SchemaResolver) MetricDefinitionsById(ctx context.Context, args struct {
	Ids []string
}) ([]*MetricDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.MetricDefinitionsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		mdr := &MetricDefinitionResolver{
			M: *md,
			S: r,
			C: ctx,
		}
		result = append(result, mdr)
	}
	return result, nil
}

// Find metric definitions by unique token.
func (r *SchemaResolver) MetricDefinitionsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*MetricDefinitionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.MetricDefinitionsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		mdr := &MetricDefinitionResolver{
			M: *md,
			S: r,
			C: ctx,
		}
		result = append(result, mdr)
	}
	return result, nil
}

// List all metric definitions that match the given criteria.
func (r *SchemaResolver) MetricDefinitions(ctx context.Context, args struct {
	Criteria model.MetricDefinitionSearchCriteria
}) (*MetricDefinitionSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.MetricDefinitions(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &MetricDefinitionSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
