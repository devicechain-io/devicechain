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

// IngestMessagesPerSecond / IngestBurst resolve the per-tenant ingest governance
// overrides; null means the tenant inherits the platform default.
func (r *AdminTenantResolver) IngestMessagesPerSecond() *float64 { return r.M.IngestMessagesPerSecond }
func (r *AdminTenantResolver) IngestBurst() *int32 {
	if r.M.IngestBurst == nil {
		return nil
	}
	v := int32(*r.M.IngestBurst)
	return &v
}

// OutboundMessagesPerSecond / OutboundBurst resolve the per-tenant outbound
// governance overrides (ADR-060 SD-3); null means the tenant inherits the
// platform default.
func (r *AdminTenantResolver) OutboundMessagesPerSecond() *float64 {
	return r.M.OutboundMessagesPerSecond
}
func (r *AdminTenantResolver) OutboundBurst() *int32 {
	if r.M.OutboundBurst == nil {
		return nil
	}
	v := int32(*r.M.OutboundBurst)
	return &v
}

// AiExternalEnabled resolves the per-tenant external-AI consent (ADR-056 §6) for
// the operator's visibility/edit: the raw nullable column, where null (or false)
// means the tenant is not opted in.
func (r *AdminTenantResolver) AiExternalEnabled() *bool { return r.M.AiExternalEnabled }

func (r *AdminTenantResolver) AiInferenceRequestsPerMinute() *float64 {
	return r.M.AiInferenceRequestsPerMinute
}

func (r *AdminTenantResolver) AiInferenceBurst() *int32 {
	if r.M.AiInferenceBurst == nil {
		return nil
	}
	v := int32(*r.M.AiInferenceBurst)
	return &v
}

// Config resolves the AdminTenant.config field: the freeform config map as a
// JSON object string, or null when unset.
func (r *AdminTenantResolver) Config() (*string, error) { return marshalConfig(r.M.Config) }

// marshalConfig renders a config map as a JSON object string, or null when empty —
// the inverse of parseConfig, shared by the tenant and tier config fields.
func marshalConfig(cfg map[string]any) (*string, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(cfg)
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

// Authorities lists the authority vocabulary so the console can offer a checklist
// when defining a role (requires role:read).
//
// With a scope it lists only what a role at that scope may actually GRANT
// (ADR-065): an authority's tier and a role's scope must agree, so an unfiltered
// checklist would offer a tenant role ai:admin and let the operator discover the
// rule from a save error. The argument is optional and an absent scope still
// returns the whole vocabulary — the console's role editor is the only caller that
// has a scope to give, and a caller that just wants the vocabulary should not be
// forced to invent one.
func (r *AdminResolver) Authorities(ctx context.Context, args struct{ Scope *string }) ([]string, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	if args.Scope == nil {
		return auth.Authorities(), nil
	}
	scope := iam.RoleScope(*args.Scope)
	if !scope.Valid() {
		return nil, fmt.Errorf("invalid role scope %q (want %q or %q)", *args.Scope, iam.ScopeSystem, iam.ScopeTenant)
	}
	if scope == iam.ScopeSystem {
		return auth.AuthoritiesForScope(auth.TierSystem), nil
	}
	return auth.AuthoritiesForScope(auth.TierTenant), nil
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
	Token                        string
	Name                         *string
	TierToken                    string
	Config                       *string
	IngestMessagesPerSecond      *float64
	IngestBurst                  *int32
	OutboundMessagesPerSecond    *float64
	OutboundBurst                *int32
	AiExternalEnabled            *bool
	AiInferenceRequestsPerMinute *float64
	AiInferenceBurst             *int32
}

// adminTenantUpdateInput mirrors AdminTenantUpdateRequest.
type adminTenantUpdateInput struct {
	Name                         *string
	TierToken                    string
	Config                       *string
	IngestMessagesPerSecond      *float64
	IngestBurst                  *int32
	OutboundMessagesPerSecond    *float64
	OutboundBurst                *int32
	AiExternalEnabled            *bool
	AiInferenceRequestsPerMinute *float64
	AiInferenceBurst             *int32
}

// intPtr adapts an optional GraphQL Int (*int32) to the model's *int, preserving
// nil (inherit-the-default) rather than coercing it to zero.
func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
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
		Token: args.Request.Token, Name: strOrEmpty(args.Request.Name),
		TierToken: args.Request.TierToken, Config: cfg,
		GovernanceOverrides: admin.GovernanceOverrides{
			IngestMessagesPerSecond:      args.Request.IngestMessagesPerSecond,
			IngestBurst:                  intPtr(args.Request.IngestBurst),
			OutboundMessagesPerSecond:    args.Request.OutboundMessagesPerSecond,
			OutboundBurst:                intPtr(args.Request.OutboundBurst),
			AiInferenceRequestsPerMinute: args.Request.AiInferenceRequestsPerMinute,
			AiInferenceBurst:             intPtr(args.Request.AiInferenceBurst),
		},
		AiExternalEnabled: args.Request.AiExternalEnabled,
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
		Name: strOrEmpty(args.Request.Name), TierToken: args.Request.TierToken, Config: cfg,
		GovernanceOverrides: admin.GovernanceOverrides{
			IngestMessagesPerSecond:      args.Request.IngestMessagesPerSecond,
			IngestBurst:                  intPtr(args.Request.IngestBurst),
			OutboundMessagesPerSecond:    args.Request.OutboundMessagesPerSecond,
			OutboundBurst:                intPtr(args.Request.OutboundBurst),
			AiInferenceRequestsPerMinute: args.Request.AiInferenceRequestsPerMinute,
			AiInferenceBurst:             intPtr(args.Request.AiInferenceBurst),
		},
		AiExternalEnabled: args.Request.AiExternalEnabled,
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
