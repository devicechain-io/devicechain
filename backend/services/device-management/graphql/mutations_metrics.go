// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new metric definition.
func (r *SchemaResolver) CreateMetricDefinition(ctx context.Context, args struct {
	Request *model.MetricDefinitionCreateRequest
}) (*MetricDefinitionResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateMetricDefinition(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	md := &MetricDefinitionResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return md, nil
}

// Update an existing metric definition.
func (r *SchemaResolver) UpdateMetricDefinition(ctx context.Context, args struct {
	Token   string
	Request *model.MetricDefinitionCreateRequest
}) (*MetricDefinitionResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateMetricDefinition(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}

	md := &MetricDefinitionResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return md, nil
}
