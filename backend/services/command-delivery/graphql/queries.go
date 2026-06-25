// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CommandsById finds commands by unique id.
func (r *SchemaResolver) CommandsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CommandResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.CommandsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*CommandResolver, 0)
	for _, cmd := range found {
		result = append(result, &CommandResolver{
			M: *cmd,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// CommandsByToken finds commands by unique token.
func (r *SchemaResolver) CommandsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*CommandResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.CommandsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*CommandResolver, 0)
	for _, cmd := range found {
		result = append(result, &CommandResolver{
			M: *cmd,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// Commands lists all commands that match the given criteria.
func (r *SchemaResolver) Commands(ctx context.Context, args struct {
	Criteria model.CommandSearchCriteria
}) (*CommandSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.Commands(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	return &CommandSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
