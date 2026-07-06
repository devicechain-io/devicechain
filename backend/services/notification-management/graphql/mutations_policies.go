// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// CreateNotificationPolicy creates a routing policy and its rule set.
func (r *SchemaResolver) CreateNotificationPolicy(ctx context.Context, args struct {
	Request model.NotificationPolicyCreateRequest
}) (*NotificationPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	created, err := api.CreateNotificationPolicy(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &NotificationPolicyResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateNotificationPolicy updates a routing policy (replacing its rule set) by token.
func (r *SchemaResolver) UpdateNotificationPolicy(ctx context.Context, args struct {
	Token   string
	Request model.NotificationPolicyCreateRequest
}) (*NotificationPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	updated, err := api.UpdateNotificationPolicy(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &NotificationPolicyResolver{M: *updated, S: r, C: ctx}, nil
}

// DeleteNotificationPolicy hard-deletes a routing policy and its rules by token.
func (r *SchemaResolver) DeleteNotificationPolicy(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return false, err
	}
	api := r.GetApi(ctx)
	return api.DeleteNotificationPolicy(ctx, args.Token)
}
