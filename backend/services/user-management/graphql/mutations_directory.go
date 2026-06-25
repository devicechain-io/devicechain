// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/model"
)

// CreateRole creates a role granting a set of authorities (requires role:write).
func (r *SchemaResolver) CreateRole(ctx context.Context, args struct {
	Request *model.RoleCreateRequest
}) (*RoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	role, err := r.getIdentityManager(ctx).CreateRole(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &RoleResolver{M: *role, C: ctx}, nil
}

// UpdateRole updates a role by token (requires role:write).
func (r *SchemaResolver) UpdateRole(ctx context.Context, args struct {
	Token   string
	Request *model.RoleCreateRequest
}) (*RoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	role, err := r.getIdentityManager(ctx).UpdateRole(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}
	return &RoleResolver{M: *role, C: ctx}, nil
}

// DeleteRole removes a role by token (requires role:write).
func (r *SchemaResolver) DeleteRole(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return false, err
	}
	return r.getIdentityManager(ctx).DeleteRole(ctx, args.Token)
}

// CreateUser creates a user (requires user:write).
func (r *SchemaResolver) CreateUser(ctx context.Context, args struct {
	Request *model.UserCreateRequest
}) (*UserResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	user, err := r.getIdentityManager(ctx).CreateUser(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &UserResolver{M: *user, C: ctx}, nil
}

// SetUserRoles replaces a user's role assignments (requires user:write).
func (r *SchemaResolver) SetUserRoles(ctx context.Context, args struct {
	Username   string
	RoleTokens []string
}) (*UserResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	user, err := r.getIdentityManager(ctx).SetUserRoles(ctx, args.Username, args.RoleTokens)
	if err != nil {
		return nil, err
	}
	return &UserResolver{M: *user, C: ctx}, nil
}
