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
	if err := authorizeGroupBatchReads(ctx, found); err != nil {
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
	if err := authorizeGroupBatchReads(ctx, found); err != nil {
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
	// The search returns values rather than pointers, so the page is adapted to the
	// shared check instead of the check growing a second signature for one caller.
	page := make([]*model.CommandBatch, 0, len(found.Results))
	for i := range found.Results {
		page = append(page, &found.Results[i])
	}
	if err := authorizeGroupBatchReads(ctx, page); err != nil {
		return nil, err
	}

	return &CommandBatchSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// authorizeGroupBatchReads requires device:read if ANY of these records is a group batch.
//
// 🔑 IT MIRRORS THE RECORD GATE ON createCommandBatch, AND FOR THE SAME REASON: a group
// batch's record carries the group token, the group's size in resolved/accepted, and a
// refusal sample naming devices — the membership this service learned under its OWN
// device:read service identity. Reading it back is the same disclosure as provoking it,
// so it answers to the same authority.
//
// ⚠️ IT CANNOT FIRE FOR ANY TOKEN THE PLATFORM MINTS TODAY, and that is stated here so
// nobody mistakes it for a live guard or deletes it as dead code. Tenant access tokens
// get device:read and command:read together from the viewer baseline, and the one service
// token holding command:read holds device:read too; REACT holds command:write alone and
// so cannot reach these queries at all. The gate exists because that coupling lives in
// ANOTHER service's token minting, where nothing tells the next person editing it that a
// group's membership became readable one service over the moment they unbundled a read
// authority. Fail closed on our own surface instead of depending on their baseline.
func authorizeGroupBatchReads(ctx context.Context, batches []*model.CommandBatch) error {
	for _, batch := range batches {
		if batch != nil && batch.TargetKind == string(model.BatchTargetGroup) {
			return auth.Authorize(ctx, auth.DeviceRead)
		}
	}
	return nil
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
