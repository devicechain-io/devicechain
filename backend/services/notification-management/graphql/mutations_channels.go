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

// UpdateNotificationChannel applies a partial update to a delivery channel: a field the
// request omits is left alone, an explicit null clears it, a value sets it.
func (r *SchemaResolver) UpdateNotificationChannel(ctx context.Context, args struct {
	Token   string
	Request model.NotificationChannelUpdateRequest
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

// RenameNotificationChannel moves a delivery channel to a new token. It takes the same
// authority as an update — a rename is an edit of the channel, not a new kind of act, and
// inventing a second authority for it would give a deployment two answers to one question.
func (r *SchemaResolver) RenameNotificationChannel(ctx context.Context, args struct {
	Token    string
	NewToken string
}) (*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	renamed, err := api.RenameNotificationChannel(ctx, args.Token, args.NewToken)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelResolver{M: *renamed, S: r, C: ctx}, nil
}

// GenerateNotificationChannelSigningSecret mints a webhook channel's signing key and reveals it once.
func (r *SchemaResolver) GenerateNotificationChannelSigningSecret(ctx context.Context, args struct {
	Token string
}) (*IssuedChannelSigningSecretResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	issued, err := r.GetApi(ctx).GenerateChannelSigningSecret(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &IssuedChannelSigningSecretResolver{M: *issued}, nil
}

// RotateNotificationChannelSigningSecret installs a new key and reveals it once; the old one keeps signing.
func (r *SchemaResolver) RotateNotificationChannelSigningSecret(ctx context.Context, args struct {
	Token string
}) (*IssuedChannelSigningSecretResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	issued, err := r.GetApi(ctx).RotateChannelSigningSecret(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &IssuedChannelSigningSecretResolver{M: *issued}, nil
}

// EndNotificationChannelSigningRotation drops the previous key.
func (r *SchemaResolver) EndNotificationChannelSigningRotation(ctx context.Context, args struct {
	Token string
}) (*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	ch, err := r.GetApi(ctx).EndChannelSigningRotation(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelResolver{M: *ch, S: r, C: ctx}, nil
}

// DeleteNotificationChannelSigningSecret removes both keys.
func (r *SchemaResolver) DeleteNotificationChannelSigningSecret(ctx context.Context, args struct {
	Token string
}) (*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationWrite); err != nil {
		return nil, err
	}
	ch, err := r.GetApi(ctx).DeleteChannelSigningSecret(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelResolver{M: *ch, S: r, C: ctx}, nil
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
