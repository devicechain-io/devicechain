// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
)

// AdminIdentityResolver resolves the AdminIdentity type from an iam.Identity.
type AdminIdentityResolver struct {
	M iam.Identity
}

func (r *AdminIdentityResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AdminIdentityResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *AdminIdentityResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }
func (r *AdminIdentityResolver) Email() string      { return r.M.Email }
func (r *AdminIdentityResolver) FirstName() *string { return optStr(r.M.FirstName) }
func (r *AdminIdentityResolver) LastName() *string  { return optStr(r.M.LastName) }
func (r *AdminIdentityResolver) Enabled() bool      { return r.M.Enabled }

func (r *AdminIdentityResolver) SystemRoles() []string { return roleTokenList(r.M.SystemRoles) }

func (r *AdminIdentityResolver) Memberships() []*AdminMembershipResolver {
	out := make([]*AdminMembershipResolver, 0, len(r.M.Memberships))
	for i := range r.M.Memberships {
		out = append(out, &AdminMembershipResolver{M: r.M.Memberships[i]})
	}
	return out
}

// AdminMembershipResolver resolves the AdminMembership type from an iam.Membership.
type AdminMembershipResolver struct {
	M iam.Membership
}

func (r *AdminMembershipResolver) Tenant() string  { return r.M.TenantId }
func (r *AdminMembershipResolver) Enabled() bool   { return r.M.Enabled }
func (r *AdminMembershipResolver) Roles() []string { return roleTokenList(r.M.TenantRoles) }

// AdminTenantResolver resolves the AdminTenant type from an iam.Tenant.
type AdminTenantResolver struct {
	M iam.Tenant
}

func (r *AdminTenantResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AdminTenantResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *AdminTenantResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }
func (r *AdminTenantResolver) Token() string      { return r.M.Token }
func (r *AdminTenantResolver) Name() *string      { return util.NullStr(r.M.Name) }
func (r *AdminTenantResolver) Enabled() bool      { return r.M.Enabled }

// Identities lists the full identity directory (requires user:read).
func (r *AdminResolver) Identities(ctx context.Context) ([]*AdminIdentityResolver, error) {
	if err := auth.Authorize(ctx, auth.UserRead); err != nil {
		return nil, err
	}
	ids, err := r.getAdminService(ctx).ListIdentities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminIdentityResolver, 0, len(ids))
	for i := range ids {
		out = append(out, &AdminIdentityResolver{M: ids[i]})
	}
	return out, nil
}

// Tenants lists every tenant (requires tenant:read).
func (r *AdminResolver) Tenants(ctx context.Context) ([]*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	tenants, err := r.getAdminService(ctx).ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminTenantResolver, 0, len(tenants))
	for i := range tenants {
		out = append(out, &AdminTenantResolver{M: tenants[i]})
	}
	return out, nil
}

// optStr maps an empty string column to a null GraphQL field.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// roleTokenList projects iam roles to their token strings for display.
func roleTokenList(roles []iam.Role) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, role.Token)
	}
	return out
}
