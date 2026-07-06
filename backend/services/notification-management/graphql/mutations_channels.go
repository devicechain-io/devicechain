// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// CreateNotificationChannel creates a delivery channel.
func (r *SchemaResolver) CreateNotificationChannel(ctx context.Context, args struct {
	Request model.NotificationChannelCreateRequest
}) (*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	created, err := api.CreateNotificationChannel(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateNotificationChannel updates a delivery channel by token.
func (r *SchemaResolver) UpdateNotificationChannel(ctx context.Context, args struct {
	Token   string
	Request model.NotificationChannelCreateRequest
}) (*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	updated, err := api.UpdateNotificationChannel(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelResolver{M: *updated, S: r, C: ctx}, nil
}

// DeleteNotificationChannel hard-deletes a delivery channel by token. It fails if
// the channel is still referenced by a policy rule.
func (r *SchemaResolver) DeleteNotificationChannel(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return false, err
	}
	api := r.GetApi(ctx)
	return api.DeleteNotificationChannel(ctx, args.Token)
}
