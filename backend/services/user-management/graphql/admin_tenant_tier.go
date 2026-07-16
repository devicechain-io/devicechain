// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
)

// AdminTenantTierResolver resolves the AdminTenantTier type from an
// iam.TenantTier (ADR-065).
type AdminTenantTierResolver struct {
	M iam.TenantTier
}

func (r *AdminTenantTierResolver) Id() gql.ID           { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AdminTenantTierResolver) CreatedAt() *string   { return util.FormatTime(r.M.CreatedAt) }
func (r *AdminTenantTierResolver) UpdatedAt() *string   { return util.FormatTime(r.M.UpdatedAt) }
func (r *AdminTenantTierResolver) Token() string        { return r.M.Token }
func (r *AdminTenantTierResolver) Name() *string        { return util.NullStr(r.M.Name) }
func (r *AdminTenantTierResolver) Description() *string { return util.NullStr(r.M.Description) }

// Config resolves the tier's settings blob as a JSON object string, or null when
// unset — the same shape as AdminTenant.config.
func (r *AdminTenantTierResolver) Config() (*string, error) { return marshalConfig(r.M.Config) }

// TenantCount counts the tenants packaged at this tier. Resolved lazily (it takes
// ctx and runs a query) so that listing tenants — where every row carries its tier
// — does not fan out into a count per row; only a caller that actually asks for the
// count pays for it, and the only such caller lists the handful of tiers.
func (r *AdminTenantTierResolver) TenantCount(ctx context.Context) (int32, error) {
	n, err := ctx.Value(ContextAdminKey).(*admin.Service).CountTenantsAtTier(ctx, r.M.ID)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// Tier resolves the tenant's tier (ADR-065). Non-null in the schema because the
// column is a NOT NULL FK: a nil here means a tenant was loaded without its tier
// preloaded, which is a bug in the read path rather than a tenant without a tier —
// so it errors rather than silently presenting an un-tiered tenant.
func (r *AdminTenantResolver) Tier() (*AdminTenantTierResolver, error) {
	if r.M.Tier == nil {
		return nil, fmt.Errorf("tenant %q loaded without its tier", r.M.Token)
	}
	return &AdminTenantTierResolver{M: *r.M.Tier}, nil
}

// TenantTiers lists the tier catalog (requires tenant:read).
func (r *AdminResolver) TenantTiers(ctx context.Context) ([]*AdminTenantTierResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	tiers, err := r.getAdminService(ctx).ListTenantTiers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminTenantTierResolver, 0, len(tiers))
	for i := range tiers {
		out = append(out, &AdminTenantTierResolver{M: tiers[i]})
	}
	return out, nil
}

// adminTenantTierCreateInput mirrors AdminTenantTierCreateRequest.
type adminTenantTierCreateInput struct {
	Token       string
	Name        *string
	Description *string
	Config      *string
}

// adminTenantTierUpdateInput mirrors AdminTenantTierUpdateRequest.
type adminTenantTierUpdateInput struct {
	Name        *string
	Description *string
	Config      *string
}

// CreateTenantTier registers a tier (requires tenant:write).
func (r *AdminResolver) CreateTenantTier(ctx context.Context, args struct {
	Request adminTenantTierCreateInput
}) (*AdminTenantTierResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(args.Request.Config)
	if err != nil {
		return nil, err
	}
	return wrapTier(r.getAdminService(ctx).CreateTenantTier(ctx, admin.TierInput{
		Token:       args.Request.Token,
		Name:        strOrEmpty(args.Request.Name),
		Description: strOrEmpty(args.Request.Description),
		Config:      cfg,
	}))
}

// UpdateTenantTier updates a tier by token (requires tenant:write).
func (r *AdminResolver) UpdateTenantTier(ctx context.Context, args struct {
	Token   string
	Request adminTenantTierUpdateInput
}) (*AdminTenantTierResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(args.Request.Config)
	if err != nil {
		return nil, err
	}
	return wrapTier(r.getAdminService(ctx).UpdateTenantTier(ctx, args.Token, admin.TierMutableInput{
		Name:        strOrEmpty(args.Request.Name),
		Description: strOrEmpty(args.Request.Description),
		Config:      cfg,
	}))
}

// DeleteTenantTier removes a tier; returns whether one was removed (requires
// tenant:write). Refused while tenants are still packaged at it.
func (r *AdminResolver) DeleteTenantTier(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return false, err
	}
	return r.getAdminService(ctx).DeleteTenantTier(ctx, args.Token)
}

// wrapTier adapts a service result into a resolver, avoiding a nil-deref when the
// service errored.
func wrapTier(tier *iam.TenantTier, err error) (*AdminTenantTierResolver, error) {
	if err != nil {
		return nil, err
	}
	return &AdminTenantTierResolver{M: *tier}, nil
}
