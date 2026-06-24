// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-user-management/identity"
)

// AuthTokenResolver resolves the AuthToken GraphQL type.
type AuthTokenResolver struct {
	pair *identity.TokenPair
}

func (r *AuthTokenResolver) AccessToken() string  { return r.pair.AccessToken }
func (r *AuthTokenResolver) RefreshToken() string { return r.pair.RefreshToken }
func (r *AuthTokenResolver) ExpiresAt() string    { return r.pair.ExpiresAt.UTC().Format(time.RFC3339) }

// Login authenticates a username/password and returns a token pair.
func (r *SchemaResolver) Login(ctx context.Context, args struct {
	Username string
	Password string
}) (*AuthTokenResolver, error) {
	pair, err := r.getIdentityManager(ctx).Login(ctx, args.Username, args.Password)
	if err != nil {
		return nil, err
	}
	return &AuthTokenResolver{pair: pair}, nil
}

// Refresh exchanges a refresh token for a new token pair.
func (r *SchemaResolver) Refresh(ctx context.Context, args struct {
	RefreshToken string
}) (*AuthTokenResolver, error) {
	pair, err := r.getIdentityManager(ctx).Refresh(ctx, args.RefreshToken)
	if err != nil {
		return nil, err
	}
	return &AuthTokenResolver{pair: pair}, nil
}
