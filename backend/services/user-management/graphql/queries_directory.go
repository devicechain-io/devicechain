// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/model"
)

// Users lists the tenant's users (requires user:read).
func (r *SchemaResolver) Users(ctx context.Context) ([]*UserResolver, error) {
	if err := auth.Authorize(ctx, auth.UserRead); err != nil {
		return nil, err
	}
	users, err := r.getIdentityManager(ctx).Users(ctx)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*UserResolver, 0, len(users))
	for _, u := range users {
		resolvers = append(resolvers, &UserResolver{M: *u, C: ctx})
	}
	return resolvers, nil
}

// Roles lists the tenant's roles (requires role:read).
func (r *SchemaResolver) Roles(ctx context.Context) ([]*RoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	roles, err := r.getIdentityManager(ctx).Roles(ctx)
	if err != nil {
		return nil, err
	}
	return roleResolvers(ctx, roles), nil
}

// RolesByToken looks up roles by token (requires role:read).
func (r *SchemaResolver) RolesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*RoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	roles, err := r.getIdentityManager(ctx).RolesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	return roleResolvers(ctx, roles), nil
}

// roleResolvers wraps a slice of roles for GraphQL.
func roleResolvers(ctx context.Context, roles []*model.Role) []*RoleResolver {
	resolvers := make([]*RoleResolver, 0, len(roles))
	for _, role := range roles {
		resolvers = append(resolvers, &RoleResolver{M: *role, C: ctx})
	}
	return resolvers
}
