// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
)

// adminIdentityCreateInput mirrors the AdminIdentityCreateRequest input type.
type adminIdentityCreateInput struct {
	Email       string
	Password    string
	FirstName   *string
	LastName    *string
	Enabled     bool
	SystemRoles []string
}

// CreateIdentity creates a global identity (requires user:write).
func (r *AdminResolver) CreateIdentity(ctx context.Context, args struct {
	Request adminIdentityCreateInput
}) (*AdminIdentityResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	id, err := r.getAdminService(ctx).CreateIdentity(ctx, admin.CreateIdentityInput{
		Email:       args.Request.Email,
		Password:    args.Request.Password,
		FirstName:   strOrEmpty(args.Request.FirstName),
		LastName:    strOrEmpty(args.Request.LastName),
		Enabled:     args.Request.Enabled,
		SystemRoles: args.Request.SystemRoles,
	})
	if err != nil {
		return nil, err
	}
	return &AdminIdentityResolver{M: *id}, nil
}

// SetIdentityEnabled enables or disables an identity (requires user:write).
func (r *AdminResolver) SetIdentityEnabled(ctx context.Context, args struct {
	Email   string
	Enabled bool
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.SetIdentityEnabled(ctx, args.Email, args.Enabled))
	})
}

// SetSystemRoles replaces an identity's system roles (requires user:write).
func (r *AdminResolver) SetSystemRoles(ctx context.Context, args struct {
	Email      string
	RoleTokens []string
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.SetSystemRoles(ctx, args.Email, args.RoleTokens))
	})
}

// SetPassword replaces an identity's password (requires user:write).
func (r *AdminResolver) SetPassword(ctx context.Context, args struct {
	Email    string
	Password string
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.SetPassword(ctx, args.Email, args.Password))
	})
}

// DeleteIdentity removes an identity; returns whether one was removed (requires
// user:write).
func (r *AdminResolver) DeleteIdentity(ctx context.Context, args struct {
	Email string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	return r.getAdminService(ctx).DeleteIdentity(ctx, args.Email)
}

// AddMembership binds an identity to a tenant (requires user:write).
func (r *AdminResolver) AddMembership(ctx context.Context, args struct {
	Email      string
	Tenant     string
	RoleTokens []string
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.AddMembership(ctx, args.Email, args.Tenant, args.RoleTokens))
	})
}

// SetMembershipRoles replaces a membership's tenant roles (requires user:write).
func (r *AdminResolver) SetMembershipRoles(ctx context.Context, args struct {
	Email      string
	Tenant     string
	RoleTokens []string
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.SetMembershipRoles(ctx, args.Email, args.Tenant, args.RoleTokens))
	})
}

// SetMembershipEnabled enables or disables a membership (requires user:write).
func (r *AdminResolver) SetMembershipEnabled(ctx context.Context, args struct {
	Email   string
	Tenant  string
	Enabled bool
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.SetMembershipEnabled(ctx, args.Email, args.Tenant, args.Enabled))
	})
}

// RemoveMembership removes an identity's membership in a tenant (requires
// user:write).
func (r *AdminResolver) RemoveMembership(ctx context.Context, args struct {
	Email  string
	Tenant string
}) (*AdminIdentityResolver, error) {
	return r.identityMutation(ctx, func(s *admin.Service) (*identityResult, error) {
		return wrap(s.RemoveMembership(ctx, args.Email, args.Tenant))
	})
}

// identityResult carries a service result through the shared mutation wrapper.
type identityResult = AdminIdentityResolver

// wrap adapts a service (*iam.Identity, error) result into a resolver result,
// avoiding a nil-deref when the service errored.
func wrap(id *iam.Identity, err error) (*identityResult, error) {
	if err != nil {
		return nil, err
	}
	return &AdminIdentityResolver{M: *id}, nil
}

// identityMutation runs an identity-returning admin mutation behind the
// user:write authorization check shared by all of them.
func (r *AdminResolver) identityMutation(ctx context.Context, fn func(*admin.Service) (*identityResult, error)) (*AdminIdentityResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return fn(r.getAdminService(ctx))
}

// strOrEmpty dereferences an optional string argument, mapping null to "".
func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
