// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// SetTenantSsoEnforced requires or stops requiring single sign-on for a tenant (requires
// user:write).
func (r *AdminResolver) SetTenantSsoEnforced(ctx context.Context, args struct {
	Tenant   string
	Enforced bool
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	t, err := r.sso(ctx).SetTenantSsoEnforced(ctx, args.Tenant, args.Enforced)
	if err != nil {
		return nil, err
	}
	return &AdminTenantResolver{M: *t}, nil
}

// SetIdpSessionMaxAge sets a connection's session ceiling (requires user:write).
func (r *AdminResolver) SetIdpSessionMaxAge(ctx context.Context, args struct {
	Connection string
	Seconds    int32
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).SetIdpSessionMaxAge(ctx, args.Connection, int(args.Seconds)))
}

// RevokeSessions ends every session an identity has (requires user:write).
func (r *AdminResolver) RevokeSessions(ctx context.Context, args struct{ Email string }) (int32, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return 0, err
	}
	n, err := r.sso(ctx).RevokeSessions(ctx, args.Email)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// ClearPassword removes an identity's password and ends its sessions (requires user:write).
func (r *AdminResolver) ClearPassword(ctx context.Context, args struct{ Email string }) (*AdminIdentityResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	id, err := r.sso(ctx).ClearPassword(ctx, args.Email)
	if err != nil {
		return nil, err
	}
	return &AdminIdentityResolver{M: *id}, nil
}
