// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CommandBatchesById finds command batches by unique id.
func (r *SchemaResolver) CommandBatchesById(ctx context.Context, args struct {
	Ids []string
}) ([]*CommandBatchResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CommandBatchesById(ctx, ids)
	if err != nil {
		return nil, err
	}
	return batchResolvers(r, ctx, found), nil
}

// CommandBatchesByToken finds command batches by unique token.
func (r *SchemaResolver) CommandBatchesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CommandBatchResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CommandBatchesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	return batchResolvers(r, ctx, found), nil
}

// CommandBatches lists all command batches that match the given criteria.
func (r *SchemaResolver) CommandBatches(ctx context.Context, args struct {
	Criteria model.CommandBatchSearchCriteria
}) (*CommandBatchSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.CommandBatches(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	return &CommandBatchSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// batchResolvers wraps a slice of batches for the wire.
func batchResolvers(r *SchemaResolver, ctx context.Context, batches []*model.CommandBatch) []*CommandBatchResolver {
	resolved := make([]*CommandBatchResolver, 0, len(batches))
	for _, batch := range batches {
		if batch == nil {
			continue
		}
		resolved = append(resolved, &CommandBatchResolver{
			M: *batch,
			S: r,
			C: ctx,
		})
	}
	return resolved
}
