// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
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

// TenantGovernanceResolver resolves the TenantGovernance type: a tenant's EFFECTIVE
// ceilings, already folded over its ADR-065 tier. A null field means neither the
// tenant nor its tier declares one, so the enforcing service applies the platform
// default — a null here is "inherit", never "unlimited".
//
// This is where the ADR-065 cascade is assembled, and it is deliberately assembled
// HERE rather than at the consumer. core/governance already folds whatever this
// returns onto the platform default, so resolving `tenant override ?? tier` at the
// authority makes the full cascade — per-tenant override → tier → platform default
// (decision 5) — fall out with NO change to any enforcing service, and without the
// tier (a user-management entity) leaking across a service boundary into every
// consumer of the query.
//
// The tier layer is invisible on the wire on purpose. A service enforcing a ceiling
// has no business knowing WHY the number is what it is; that provenance question is
// the admin plane's, which exposes the tier and the tenant's override separately so
// effective settings stay inspectable as tier + delta rather than an opaque merged
// blob (decision 7).
type TenantGovernanceResolver struct {
	t *iam.Tenant
}

// tierOf returns the tenant's tier, or nil when it is not loaded. The tier is a
// required FK and both read paths preload it, so nil means a caller loaded the
// tenant some other way; the fold below then simply degrades to "no tier setting",
// i.e. the pre-ADR-065 behavior of override-or-platform-default. That is the
// fail-safe direction: a missed preload costs a tier's tuning, never a ceiling.
func (r *TenantGovernanceResolver) tierOf() *iam.TenantTier { return r.t.Tier }

// rate folds one dimension's rate: the tenant's own override wins, else the tier's
// setting, else null (inherit the platform default).
func (r *TenantGovernanceResolver) rate(override *float64, dim governance.Dimension) *float64 {
	if override != nil {
		return override
	}
	return r.tierOf().RateFor(dim)
}

// burst folds one dimension's burst the same way, adapting to the GraphQL Int.
func (r *TenantGovernanceResolver) burst(override *int, dim governance.Dimension) *int32 {
	v := override
	if v == nil {
		v = r.tierOf().BurstFor(dim)
	}
	if v == nil {
		return nil
	}
	i := int32(*v)
	return &i
}

func (r *TenantGovernanceResolver) IngestMessagesPerSecond() *float64 {
	return r.rate(r.t.IngestMessagesPerSecond, governance.Ingest)
}

func (r *TenantGovernanceResolver) IngestBurst() *int32 {
	return r.burst(r.t.IngestBurst, governance.Ingest)
}

func (r *TenantGovernanceResolver) OutboundMessagesPerSecond() *float64 {
	return r.rate(r.t.OutboundMessagesPerSecond, governance.Outbound)
}

func (r *TenantGovernanceResolver) OutboundBurst() *int32 {
	return r.burst(r.t.OutboundBurst, governance.Outbound)
}

// AiExternalEnabled resolves the tenant's external-AI consent as a non-null
// boolean, fail-closed: a nil column (never opted in) reads as false. Unlike the
// rate overrides above — where nil means "inherit a platform default" — there is
// no default to inherit here; nil means "no consent". The ai-inference service
// reads this over a service token before it may route the tenant's data to an
// external model (ADR-056 §6).
func (r *TenantGovernanceResolver) AiExternalEnabled() bool {
	return r.t.AiExternalEnabled != nil && *r.t.AiExternalEnabled
}

// AiInferenceRequestsPerMinute / AiInferenceBurst resolve the tenant's AI-inference
// rate overrides (ADR-056 §6 / ADR-023). Unlike the consent flag above these are
// ordinary nullable ceilings: nil means "inherit the platform default", which is
// itself a real limit. Read by the ai-inference service over a service token.
func (r *TenantGovernanceResolver) AiInferenceRequestsPerMinute() *float64 {
	return r.rate(r.t.AiInferenceRequestsPerMinute, governance.AIInference)
}

func (r *TenantGovernanceResolver) AiInferenceBurst() *int32 {
	return r.burst(r.t.AiInferenceBurst, governance.AIInference)
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
