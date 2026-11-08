// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// CommandChannelAccessResolver exposes what the HTTP command endpoints learn about a device.
type CommandChannelAccessResolver struct{ M model.CommandChannelAccess }

func (r *CommandChannelAccessResolver) DeviceToken() string     { return r.M.DeviceToken }
func (r *CommandChannelAccessResolver) DeviceTypeToken() string { return r.M.DeviceTypeToken }
func (r *CommandChannelAccessResolver) CommandChannel() *string {
	if r.M.CommandChannel == "" {
		return nil
	}
	return &r.M.CommandChannel
}

// AuthenticateCommandChannel proves a device's access token for the HTTP command endpoints.
// Null for every way a device can fail to authenticate; an error only when the platform could
// not decide. See SDL.
func (r *SchemaResolver) AuthenticateCommandChannel(ctx context.Context, args struct {
	CredentialId string
}) (*CommandChannelAccessResolver, error) {
	// Service plane: a tenant user's token must not be able to probe device access tokens.
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	access, err := r.GetApi(ctx).AuthenticateCommandChannel(ctx, args.CredentialId, time.Now())
	if err != nil || access == nil {
		return nil, err
	}
	return &CommandChannelAccessResolver{M: *access}, nil
}

// CommandPullDevices names which of the given devices take their commands over HTTP_PULL.
func (r *SchemaResolver) CommandPullDevices(ctx context.Context, args struct {
	DeviceTokens []string
}) ([]string, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	return r.GetApi(ctx).PullDevices(ctx, args.DeviceTokens)
}
