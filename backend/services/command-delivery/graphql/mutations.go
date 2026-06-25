// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CreateCommand issues (persists) a new command to a device.
func (r *SchemaResolver) CreateCommand(ctx context.Context, args struct {
	Request *model.CommandCreateRequest
}) (*CommandResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateCommand(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	return &CommandResolver{
		M: *created,
		S: r,
		C: ctx,
	}, nil
}

// CancelCommand cancels a non-terminal command by token (moves it to EXPIRED).
func (r *SchemaResolver) CancelCommand(ctx context.Context, args struct {
	Token string
}) (*CommandResolver, error) {
	api := r.GetApi(ctx)
	cancelled, err := api.CancelCommand(ctx, args.Token)
	if err != nil {
		return nil, err
	}

	return &CommandResolver{
		M: *cancelled,
		S: r,
		C: ctx,
	}, nil
}
