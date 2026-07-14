// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
)

// TenantResolver resolves the Tenant GraphQL type: the control-plane tenant the
// caller is acting within. It backs the console's tenant header and carries the
// resolved white-labeling branding (ADR-038). It holds a back-reference to the
// schema resolver so the branding field can reach the settings service (the
// cascade's default tier).
type TenantResolver struct {
	t   *iam.Tenant
	svc *SchemaResolver
}

func (r *TenantResolver) Token() string        { return r.t.Token }
func (r *TenantResolver) Name() *string        { return util.NullStr(r.t.Name) }
func (r *TenantResolver) Description() *string { return util.NullStr(r.t.Description) }

// Tenant describes the tenant the caller is acting within (ADR-033 data plane).
// It is self-scoped — a caller only ever sees their own tenant, resolved from the
// tenant in their access token — so being authenticated to the tenant is enough.
func (r *SchemaResolver) Tenant(ctx context.Context) (*TenantResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	t, err := r.getIdentityManager(ctx).CurrentTenant(ctx, claims.Tenant)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// TenantGovernanceResolver resolves the TenantGovernance type: a tenant's ingest
// governance overrides. A null field means the tenant inherits the platform
// default (the enforcing service supplies that default), so a null here is not
// "unlimited".
type TenantGovernanceResolver struct {
	t *iam.Tenant
}

func (r *TenantGovernanceResolver) IngestMessagesPerSecond() *float64 {
	return r.t.IngestMessagesPerSecond
}

func (r *TenantGovernanceResolver) IngestBurst() *int32 {
	if r.t.IngestBurst == nil {
		return nil
	}
	v := int32(*r.t.IngestBurst)
	return &v
}

func (r *TenantGovernanceResolver) OutboundMessagesPerSecond() *float64 {
	return r.t.OutboundMessagesPerSecond
}

func (r *TenantGovernanceResolver) OutboundBurst() *int32 {
	if r.t.OutboundBurst == nil {
		return nil
	}
	v := int32(*r.t.OutboundBurst)
	return &v
}

// TenantGovernance returns the governance overrides for the tenant the caller is
// acting within — the read side of ADR-023 per-tenant limits (both the ingest and
// the ADR-060 outbound dimensions), consumed by the enforcing services over a
// service token: event-sources (ingest rate), outbound-connectors (egress rate,
// sink side), and event-processing (egress rate, REACT source side). Unlike the
// self-scoped Tenant query it keys off the tenant in *context* (stamped from the
// caller's access token or, for a service token, the X-DC-Tenant header) rather
// than a claims field, so a tenant-less service token resolves the header tenant.
// Gated on tenant:read so only a caller granted that authority can read it.
func (r *SchemaResolver) TenantGovernance(ctx context.Context) (*TenantGovernanceResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	t, err := r.getIdentityManager(ctx).CurrentTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return &TenantGovernanceResolver{t: t}, nil
}

// CurrentIdentityResolver resolves the CurrentIdentity GraphQL type: the global
// identity the caller is signed in as, for display in the console (name, falling
// back to email).
type CurrentIdentityResolver struct {
	id *iam.Identity
}

func (r *CurrentIdentityResolver) Email() string      { return r.id.Email }
func (r *CurrentIdentityResolver) FirstName() *string { return optStr(r.id.FirstName) }
func (r *CurrentIdentityResolver) LastName() *string  { return optStr(r.id.LastName) }

// Me describes the identity the caller is signed in as (ADR-033). Self-scoped:
// resolved from the email carried as the access token's subject, so being
// authenticated is enough.
func (r *SchemaResolver) Me(ctx context.Context) (*CurrentIdentityResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	id, err := r.getIdentityManager(ctx).CurrentUser(ctx, claims.Username)
	if err != nil {
		return nil, err
	}
	return &CurrentIdentityResolver{id: id}, nil
}

// IdentityMemberships re-reads the live memberships for a valid identity token
// (ADR-033), so the console can refresh its tenant picker after a membership
// change without a re-login. The identity token is passed as an argument and
// validated internally (like selectTenant), so this runs before a tenant is
// selected — there is no membership-bearing token to authenticate with yet.
func (r *SchemaResolver) IdentityMemberships(ctx context.Context, args struct {
	IdentityToken string
}) ([]*MembershipResolver, error) {
	infos, err := r.getIdentityManager(ctx).Memberships(ctx, args.IdentityToken)
	if err != nil {
		return nil, err
	}
	out := make([]*MembershipResolver, 0, len(infos))
	for i := range infos {
		out = append(out, &MembershipResolver{m: infos[i]})
	}
	return out, nil
}
