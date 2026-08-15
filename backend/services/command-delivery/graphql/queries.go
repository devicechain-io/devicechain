// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CommandsById finds commands by unique id.
func (r *SchemaResolver) CommandsById(ctx context.Context, args struct {
	Ids []string
}) ([]*CommandResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
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
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

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

// DrainableCommands returns the head of one device's still-waiting backlog — HELD and
// PARKED commands inside their expiry horizon, oldest first, bounded.
//
// 🔑 IT IS GATED ON command:claim, NOT command:read, AND THAT IS A DELIBERATE
// TIGHTENING RATHER THAN A COPY OF THE NEIGHBOURS ABOVE. command:read is tenant-tier, so
// every one of the reads on this schema is reachable by an ordinary tenant user token.
// command:claim is system-tier, which auth.satisfies() bounds to service and identity
// tokens — including against a tenant token holding "*". This query is therefore
// machine-only, exactly like markCommandSent and parkCommand.
//
// It costs the real caller nothing: lwm2m-ingest already mints command:claim, because it
// claims every row it drains before actuating it. And it buys a genuine narrowing — the
// answer here is the precise list a transport is about to turn into physical movement,
// assembled in dispatch order, which is not a thing a console user has any reason to ask
// for. The generic `commands` query still serves human backlog inspection under
// command:read; what a tenant token loses is only this dispatch-shaped view of it.
func (r *SchemaResolver) DrainableCommands(ctx context.Context, args struct {
	DeviceToken string
	Limit       *int32
}) ([]*CommandResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandClaim); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	// A null/absent limit arrives as nil and goes down as 0, which the model reads as
	// "use the default" — the same branch a caller sending 0 or a negative takes. The
	// clamp to rdb.MaxPageSize lives there too, so the bound cannot differ between this
	// resolver and any other caller of the API.
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	found, err := api.DrainableCommands(ctx, args.DeviceToken, limit)
	if err != nil {
		return nil, err
	}

	// Built by append over the model's slice, so the ORDER the model returned survives to
	// the wire. The caller dispatches in this order.
	result := make([]*CommandResolver, 0, len(found))
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
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, err
	}

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
