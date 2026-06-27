// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
)

// AdminRoleResolver resolves the AdminRole type from an iam.Role.
type AdminRoleResolver struct {
	M iam.Role
}

func (r *AdminRoleResolver) Id() gql.ID           { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AdminRoleResolver) CreatedAt() *string   { return util.FormatTime(r.M.CreatedAt) }
func (r *AdminRoleResolver) UpdatedAt() *string   { return util.FormatTime(r.M.UpdatedAt) }
func (r *AdminRoleResolver) Scope() string        { return string(r.M.Scope) }
func (r *AdminRoleResolver) Token() string        { return r.M.Token }
func (r *AdminRoleResolver) Name() *string        { return util.NullStr(r.M.Name) }
func (r *AdminRoleResolver) Description() *string { return util.NullStr(r.M.Description) }
func (r *AdminRoleResolver) Authorities() []string {
	if r.M.Authorities == nil {
		return []string{}
	}
	return r.M.Authorities
}

// Config resolves the AdminTenant.config field: the freeform config map as a
// JSON object string, or null when unset.
func (r *AdminTenantResolver) Config() (*string, error) {
	if len(r.M.Config) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(r.M.Config)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// Roles lists the role catalog, optionally filtered to a scope (requires
// role:read).
func (r *AdminResolver) Roles(ctx context.Context, args struct{ Scope *string }) ([]*AdminRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	var scope *iam.RoleScope
	if args.Scope != nil {
		rs := iam.RoleScope(*args.Scope)
		if !rs.Valid() {
			return nil, fmt.Errorf("invalid role scope %q (want %q or %q)", *args.Scope, iam.ScopeSystem, iam.ScopeTenant)
		}
		scope = &rs
	}
	roles, err := r.getAdminService(ctx).ListRoles(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminRoleResolver, 0, len(roles))
	for i := range roles {
		out = append(out, &AdminRoleResolver{M: roles[i]})
	}
	return out, nil
}

// adminRoleCreateInput mirrors AdminRoleCreateRequest.
type adminRoleCreateInput struct {
	Scope       string
	Token       string
	Name        *string
	Description *string
	Authorities []string
}

// adminRoleUpdateInput mirrors AdminRoleUpdateRequest.
type adminRoleUpdateInput struct {
	Name        *string
	Description *string
	Authorities []string
}

// CreateRole creates a role (requires role:write).
func (r *AdminResolver) CreateRole(ctx context.Context, args struct {
	Request adminRoleCreateInput
}) (*AdminRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	role, err := r.getAdminService(ctx).CreateRole(ctx, admin.RoleInput{
		Scope:       args.Request.Scope,
		Token:       args.Request.Token,
		Name:        strOrEmpty(args.Request.Name),
		Description: strOrEmpty(args.Request.Description),
		Authorities: args.Request.Authorities,
	})
	return wrapRole(role, err)
}

// UpdateRole updates a role by scope + token (requires role:write).
func (r *AdminResolver) UpdateRole(ctx context.Context, args struct {
	Scope   string
	Token   string
	Request adminRoleUpdateInput
}) (*AdminRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	role, err := r.getAdminService(ctx).UpdateRole(ctx, args.Scope, args.Token, admin.RoleMutableInput{
		Name:        strOrEmpty(args.Request.Name),
		Description: strOrEmpty(args.Request.Description),
		Authorities: args.Request.Authorities,
	})
	return wrapRole(role, err)
}

// DeleteRole removes a role; returns whether one was removed (requires
// role:write).
func (r *AdminResolver) DeleteRole(ctx context.Context, args struct {
	Scope string
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return false, err
	}
	return r.getAdminService(ctx).DeleteRole(ctx, args.Scope, args.Token)
}

// adminTenantCreateInput mirrors AdminTenantCreateRequest.
type adminTenantCreateInput struct {
	Token  string
	Name   *string
	Config *string
}

// adminTenantUpdateInput mirrors AdminTenantUpdateRequest.
type adminTenantUpdateInput struct {
	Name   *string
	Config *string
}

// CreateTenant registers a tenant (requires tenant:write).
func (r *AdminResolver) CreateTenant(ctx context.Context, args struct {
	Request adminTenantCreateInput
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(args.Request.Config)
	if err != nil {
		return nil, err
	}
	tenant, err := r.getAdminService(ctx).CreateTenant(ctx, admin.TenantInput{
		Token: args.Request.Token, Name: strOrEmpty(args.Request.Name), Config: cfg,
	})
	return wrapTenant(tenant, err)
}

// UpdateTenant updates a tenant's name + config (requires tenant:write).
func (r *AdminResolver) UpdateTenant(ctx context.Context, args struct {
	Token   string
	Request adminTenantUpdateInput
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(args.Request.Config)
	if err != nil {
		return nil, err
	}
	tenant, err := r.getAdminService(ctx).UpdateTenant(ctx, args.Token, admin.TenantMutableInput{
		Name: strOrEmpty(args.Request.Name), Config: cfg,
	})
	return wrapTenant(tenant, err)
}

// SetTenantEnabled enables or disables a tenant (requires tenant:write).
func (r *AdminResolver) SetTenantEnabled(ctx context.Context, args struct {
	Token   string
	Enabled bool
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	return wrapTenant(r.getAdminService(ctx).SetTenantEnabled(ctx, args.Token, args.Enabled))
}

// DeleteTenant removes a tenant; returns whether one was removed (requires
// tenant:write).
func (r *AdminResolver) DeleteTenant(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return false, err
	}
	return r.getAdminService(ctx).DeleteTenant(ctx, args.Token)
}

// wrapRole / wrapTenant adapt a service result into a resolver, avoiding a
// nil-deref when the service errored.
func wrapRole(role *iam.Role, err error) (*AdminRoleResolver, error) {
	if err != nil {
		return nil, err
	}
	return &AdminRoleResolver{M: *role}, nil
}

func wrapTenant(tenant *iam.Tenant, err error) (*AdminTenantResolver, error) {
	if err != nil {
		return nil, err
	}
	return &AdminTenantResolver{M: *tenant}, nil
}

// parseConfig decodes an optional JSON object string into a config map. A null or
// empty argument yields a nil map (no config); a non-object or malformed JSON is
// an error.
func parseConfig(s *string) (map[string]any, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(*s), &cfg); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %w", err)
	}
	return cfg, nil
}
