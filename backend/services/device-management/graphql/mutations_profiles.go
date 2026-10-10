// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a new device profile.
func (r *SchemaResolver) CreateDeviceProfile(ctx context.Context, args struct {
	Request *model.DeviceProfileCreateRequest
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDeviceProfile(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing device profile. The profile is named by the token ARGUMENT and
// the request carries none, so this can never retarget a second profile.
func (r *SchemaResolver) UpdateDeviceProfile(ctx context.Context, args struct {
	Token   string
	Request model.DeviceProfileUpdateRequest
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDeviceProfile(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *updated, S: r, C: ctx}, nil
}

// ConfigurationKeyInput is one key of a configuration declaration.
type ConfigurationKeyInput struct {
	Key         string
	ValueType   string
	Description *string
}

// SetDeviceProfileConfigurationDeclaration replaces a profile's DRAFT device-visible
// configuration declaration. Same authority as any other profile edit.
func (r *SchemaResolver) SetDeviceProfileConfigurationDeclaration(ctx context.Context, args struct {
	Token string
	Keys  []ConfigurationKeyInput
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	keys := make([]model.ConfigurationKey, 0, len(args.Keys))
	for _, k := range args.Keys {
		key := model.ConfigurationKey{Key: k.Key, ValueType: k.ValueType}
		if k.Description != nil {
			key.Description = *k.Description
		}
		keys = append(keys, key)
	}
	updated, err := r.GetApi(ctx).SetDeviceProfileConfigurationDeclaration(ctx, args.Token, keys)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *updated, S: r, C: ctx}, nil
}

// RenameDeviceProfile changes a profile's token and nothing else. It is the capability
// updateDeviceProfile's payload token used to carry, given a mutation where the new
// token can mean only one thing. The authority is updateDeviceProfile's — a rename is
// an edit of the profile, not a new kind of act.
func (r *SchemaResolver) RenameDeviceProfile(ctx context.Context, args struct {
	Token    string
	NewToken string
}) (*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	renamed, err := api.RenameDeviceProfile(ctx, args.Token, args.NewToken)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileResolver{M: *renamed, S: r, C: ctx}, nil
}
