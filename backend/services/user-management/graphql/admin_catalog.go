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

// PurgeState / PurgeEpoch resolve the ADR-077 deletion lifecycle. They are what let
// the console say something true about a deleted tenant: the row is still here, it
// admits nobody, its token is reserved, and its data is being reclaimed separately.
func (r *AdminTenantResolver) PurgeState() string { return string(r.M.PurgeState) }

// PurgeEpoch goes through formatPurgeTime, the SAME formatter the deletion record's own
// `epoch` uses. That is load-bearing rather than tidiness: the epoch is half of a deletion's
// identity, and a console correlating this badge with a record in the deletion history
// compares these two strings. Two call sites formatting "the same way" is the arrangement in
// which one later gains a nanosecond and the two silently stop matching.
func (r *AdminTenantResolver) PurgeEpoch() *string { return formatPurgeTimePtr(r.M.PurgeEpoch) }

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

// ShedPriority resolves the per-tenant ADR-063 shed-priority override (1–100) for the
// operator's visibility/edit — the RAW nullable column, null when the tenant inherits
// its tier's shedPriority. This read-back is load-bearing: updateTenant is a
// full-REPLACE of the mutable fields (applyTo writes every override, nil clearing it),
// so a console that could not read the current override back would null it on any
// unrelated edit — silently erasing an operator's "degrades last" placement. Every
// other override is readable here for the same reason; this one must be too.
func (r *AdminTenantResolver) ShedPriority() *int32 {
	if r.M.ShedPriority == nil {
		return nil
	}
	v := int32(*r.M.ShedPriority)
	return &v
}

// HeldCommandCeiling resolves the per-tenant HELD-command ceiling override for the
// operator's visibility/edit — the RAW nullable column, null when the tenant inherits
// its tier's heldCommandCeiling. Load-bearing for the same reason as ShedPriority
// above: updateTenant is a full REPLACE of the mutable fields (applyTo writes every
// override, nil clearing it), so a console that could not read the current value back
// would null it on any unrelated edit — quietly returning a tenant that was deliberately
// bounded to whatever the tier or the service default happens to be.
func (r *AdminTenantResolver) HeldCommandCeiling() *int32 {
	if r.M.HeldCommandCeiling == nil {
		return nil
	}
	v := int32(*r.M.HeldCommandCeiling)
	return &v
}

// The three per-tenant geofence cap overrides, as the RAW nullable columns — null when the
// tenant inherits its tier's key. Load-bearing for the same reason as ShedPriority and
// HeldCommandCeiling above: updateTenant is a full REPLACE of the mutable fields (applyTo
// writes every override, nil clearing it), so a console that could not read the current value
// back would null it on any unrelated edit. Here that would silently RAISE a cap an operator
// had deliberately tightened, which is the one direction this whole feature exists to prevent.

// GeoFencePositionCeiling resolves the per-tenant per-fence position ceiling override.
func (r *AdminTenantResolver) GeoFencePositionCeiling() *int32 {
	return rawInt32(r.M.GeoFencePositionCeiling)
}

// GeoFenceCeiling resolves the per-tenant fence-count override.
func (r *AdminTenantResolver) GeoFenceCeiling() *int32 { return rawInt32(r.M.GeoFenceCeiling) }

// GeoFencePositionBudget resolves the per-tenant whole-fence-set position budget override.
func (r *AdminTenantResolver) GeoFencePositionBudget() *int32 {
	return rawInt32(r.M.GeoFencePositionBudget)
}

// rawInt32 adapts a nullable model int to the wire's nullable Int, preserving nil (inherit)
// rather than coercing it to zero — which the cascade reads as "not a cap" and the console
// would render as a configured bound of none.
func rawInt32(v *int) *int32 {
	if v == nil {
		return nil
	}
	i := int32(*v)
	return &i
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

// 🔴 THERE IS NO adminRoleUpdateInput ANY MORE, AND ITS ABSENCE IS THE POINT.
//
// Each update mutation used to declare a resolver-local mirror of its SDL input and
// flatten it into an admin.*MutableInput, which is two hops for the three states to
// survive and one place for them to be lost — a mirror of plain *string fields fed into
// a full-replace struct is precisely the "converted in name only" shape core's
// exhaustiveness guard was rewritten to catch. The resolver now takes the service's own
// *UpdateRequest directly, so graphql-go packs the wire request straight into the type
// the service applies and there is no intermediate representation to drop a state in.

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

// UpdateRole applies a partial update to the role named by scope + token (requires
// role:write). Omitted fields are left alone, an explicit null clears, a value sets.
//
// Both scope and token stay mutation ARGUMENTS: a role's identity is the pair, and the
// request carries neither, so addressing a second role through the payload is
// unrepresentable.
func (r *AdminResolver) UpdateRole(ctx context.Context, args struct {
	Scope   string
	Token   string
	Request admin.RoleUpdateRequest
}) (*AdminRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	role, err := r.getAdminService(ctx).UpdateRole(ctx, args.Scope, args.Token, &args.Request)
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
	ShedPriority                 *int32
	HeldCommandCeiling           *int32
	GeoFencePositionCeiling      *int32
	GeoFenceCeiling              *int32
	GeoFencePositionBudget       *int32
}

// intPtr adapts an optional GraphQL Int (*int32) to the model's *int, preserving
// nil (inherit-the-default) rather than coercing it to zero. Used by the CREATE
// resolvers, which have nothing to preserve and so still take plain pointers; the
// update path's counterpart is patch.IntPtr, which folds three states rather than two.
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
			ShedPriority:                 intPtr(args.Request.ShedPriority),
			HeldCommandCeiling:           intPtr(args.Request.HeldCommandCeiling),
			GeoFencePositionCeiling:      intPtr(args.Request.GeoFencePositionCeiling),
			GeoFenceCeiling:              intPtr(args.Request.GeoFenceCeiling),
			GeoFencePositionBudget:       intPtr(args.Request.GeoFencePositionBudget),
		},
		AiExternalEnabled: args.Request.AiExternalEnabled,
	})
	return wrapTenant(tenant, err)
}

// UpdateTenant applies a partial update to a tenant (requires tenant:write): an omitted
// field leaves the stored value alone, an explicit null clears it, a value sets it.
//
// 🔴 The governance overrides are the reason this matters most. Under the full-replace
// shape every omitted override was written as NULL, so a rename silently removed a
// tenant's ingest ceiling, its shed priority and its geofence caps — which is why the
// AdminTenant type exposes each raw column, so the console could round-trip them. Those
// read-backs are still useful; they are no longer load-bearing.
func (r *AdminResolver) UpdateTenant(ctx context.Context, args struct {
	Token   string
	Request admin.TenantUpdateRequest
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	tenant, err := r.getAdminService(ctx).UpdateTenant(ctx, args.Token, &args.Request)
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

// parseConfig decodes an optional JSON object string into a config map for the CREATE
// resolvers. It delegates to admin.ParseConfigJSON, which the update path also uses, so
// the two doors cannot come to disagree about what an empty config is: a second copy
// here is how "{}" would end up meaning NULL on one path and an empty document on the
// other, for the same tenant.
func parseConfig(s *string) (map[string]any, error) { return admin.ParseConfigJSON(s) }
