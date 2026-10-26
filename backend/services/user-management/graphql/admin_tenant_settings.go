// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/governance"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
)

// The admin plane's answer to "what is this tenant actually metered at, and why?"
// (ADR-065 decision 7): effective settings resolved as tier + delta, never an opaque
// merged blob.
//
// Every value here comes from iam.Tenant's cascade — the SAME code the data-plane
// tenantGovernance query resolves an enforcing service's ceiling through. That is
// the point of the surface: an operator screen that re-derived the cascade would
// eventually tell them something the platform does not do.

// AdminGovernanceDimensionResolver resolves one platform governance dimension.
type AdminGovernanceDimensionResolver struct {
	D governance.Dimension
}

func (r *AdminGovernanceDimensionResolver) Name() string       { return r.D.Name }
func (r *AdminGovernanceDimensionResolver) Label() string      { return r.D.Label }
func (r *AdminGovernanceDimensionResolver) RateField() string  { return r.D.RateField }
func (r *AdminGovernanceDimensionResolver) BurstField() string { return r.D.BurstField }
func (r *AdminGovernanceDimensionResolver) RateUnit() string   { return r.D.RateUnit }

// GovernanceDimensions lists the platform's governance dimensions (requires
// tenant:read — the same authority the tier catalog is gated on, since this exists
// to describe what a tier may carry).
//
// Derived from governance.AllDimensions(), so it is the platform's own vocabulary
// rather than a restatement of it.
func (r *AdminResolver) GovernanceDimensions(ctx context.Context) ([]*AdminGovernanceDimensionResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	dims := governance.AllDimensions()
	out := make([]*AdminGovernanceDimensionResolver, 0, len(dims))
	for _, d := range dims {
		out = append(out, &AdminGovernanceDimensionResolver{D: d})
	}
	return out, nil
}

// AdminRateSettingResolver resolves one dimension's effective rate with provenance.
type AdminRateSettingResolver struct {
	source   iam.SettingSource
	value    *float64
	tier     *float64
	override *float64
}

func (r *AdminRateSettingResolver) Source() string     { return string(r.source) }
func (r *AdminRateSettingResolver) Value() *float64    { return r.value }
func (r *AdminRateSettingResolver) Tier() *float64     { return r.tier }
func (r *AdminRateSettingResolver) Override() *float64 { return r.override }

// AdminBurstSettingResolver resolves one dimension's effective burst with
// provenance, adapting to the GraphQL Int.
type AdminBurstSettingResolver struct {
	source   iam.SettingSource
	value    *int
	tier     *int
	override *int
}

func (r *AdminBurstSettingResolver) Source() string { return string(r.source) }

// Value, Tier and Override adapt each level's optional int to the GraphQL Int through
// util.IntPtrInt32: nil stays nil ("this level declares none" — never zero, which would
// read as a ceiling admitting nothing), and a value the Int cannot hold is refused rather
// than wrapped.
func (r *AdminBurstSettingResolver) Value() (*int32, error) {
	return util.IntPtrInt32("value", r.value)
}
func (r *AdminBurstSettingResolver) Tier() (*int32, error) { return util.IntPtrInt32("tier", r.tier) }
func (r *AdminBurstSettingResolver) Override() (*int32, error) {
	return util.IntPtrInt32("override", r.override)
}

// AdminTenantSettingResolver resolves what a tenant is metered at for one dimension.
type AdminTenantSettingResolver struct {
	dim   governance.Dimension
	rate  *AdminRateSettingResolver
	burst *AdminBurstSettingResolver
}

func (r *AdminTenantSettingResolver) Dimension() *AdminGovernanceDimensionResolver {
	return &AdminGovernanceDimensionResolver{D: r.dim}
}
func (r *AdminTenantSettingResolver) Rate() *AdminRateSettingResolver   { return r.rate }
func (r *AdminTenantSettingResolver) Burst() *AdminBurstSettingResolver { return r.burst }

// EffectiveSettings resolves the tenant's effective settings for every governance
// dimension the platform declares.
//
// It enumerates governance.AllDimensions() rather than listing dimensions here, so
// a fourth appears on the operator's screen the day it is declared — and
// iam.Tenant's cascade fails loud for a dimension whose override columns are
// missing, rather than quietly reporting it as un-overridden.
//
// No authorization check of its own: this is a field on AdminTenant, and reaching an
// AdminTenant already required tenant:read at the query that returned it. Adding a
// second check here would not tighten anything (there is no path to this resolver
// that skips the first) — the tenant queries are the door.
func (r *AdminTenantResolver) EffectiveSettings() []*AdminTenantSettingResolver {
	dims := governance.AllDimensions()
	out := make([]*AdminTenantSettingResolver, 0, len(dims))
	for _, d := range dims {
		rateValue, rateSource := r.M.EffectiveRate(d)
		burstValue, burstSource := r.M.EffectiveBurst(d)
		out = append(out, &AdminTenantSettingResolver{
			dim: d,
			rate: &AdminRateSettingResolver{
				source:   rateSource,
				value:    rateValue,
				tier:     r.M.Tier.RateFor(d),
				override: r.M.OverrideRate(d),
			},
			burst: &AdminBurstSettingResolver{
				source:   burstSource,
				value:    burstValue,
				tier:     r.M.Tier.BurstFor(d),
				override: r.M.OverrideBurst(d),
			},
		})
	}
	return out
}

// AdminTenantScalarSettingResolver resolves one tier-governed scalar's effective value with
// the same provenance shape as the burst: winner and source, with the tier's value and the
// tenant's override beside it.
type AdminTenantScalarSettingResolver struct {
	key      string
	label    string
	source   iam.SettingSource
	value    *int
	tier     *int
	override *int
}

func (r *AdminTenantScalarSettingResolver) Key() string    { return r.key }
func (r *AdminTenantScalarSettingResolver) Label() string  { return r.label }
func (r *AdminTenantScalarSettingResolver) Source() string { return string(r.source) }

// Value, Tier and Override adapt each level's optional int to the GraphQL Int exactly as the
// burst does (nil stays nil; a value the Int cannot hold is refused, never wrapped).
func (r *AdminTenantScalarSettingResolver) Value() (*int32, error) {
	return util.IntPtrInt32("value", r.value)
}
func (r *AdminTenantScalarSettingResolver) Tier() (*int32, error) {
	return util.IntPtrInt32("tier", r.tier)
}
func (r *AdminTenantScalarSettingResolver) Override() (*int32, error) {
	return util.IntPtrInt32("override", r.override)
}

// EffectiveScalarSettings resolves the tenant's effective value for each tier-governed scalar
// that is not a rate+burst dimension: the held-command ceiling, the shed priority and the three
// geofence caps. Each resolves through the same iam.Tenant cascade method the data plane reads
// (Effective*), with the tier's and the override's own values beside the winner.
//
// The set is listed here rather than derived because each scalar is a typed field with its own
// cascade method (see iam/cascade.go); TestEffectiveScalarSettingsCoversEveryTierKey ties the
// keys to the tier config registry so a new scalar cannot be registered without appearing here.
// Like EffectiveSettings it carries no authorization of its own: reaching an AdminTenant already
// required tenant:read.
func (r *AdminTenantResolver) EffectiveScalarSettings() []*AdminTenantScalarSettingResolver {
	m := &r.M
	row := func(key, label string, eff func() (*int, iam.SettingSource), tier, override *int) *AdminTenantScalarSettingResolver {
		v, src := eff()
		return &AdminTenantScalarSettingResolver{key: key, label: label, source: src, value: v, tier: tier, override: override}
	}
	return []*AdminTenantScalarSettingResolver{
		row(iam.HeldCommandCeilingConfigKey, "Held command ceiling", m.EffectiveHeldCommandCeiling,
			m.Tier.HeldCommandCeiling(), m.OverrideHeldCommandCeiling()),
		row(iam.ShedPriorityConfigKey, "Shed priority", m.EffectiveShedPriority,
			m.Tier.ShedPriority(), m.OverrideShedPriority()),
		row(iam.GeoFenceCeilingConfigKey, "Geofence count ceiling", m.EffectiveGeoFenceCeiling,
			m.Tier.GeoFenceCeiling(), m.OverrideGeoFenceCeiling()),
		row(iam.GeoFencePositionCeilingConfigKey, "Geofence position ceiling", m.EffectiveGeoFencePositionCeiling,
			m.Tier.GeoFencePositionCeiling(), m.OverrideGeoFencePositionCeiling()),
		row(iam.GeoFencePositionBudgetConfigKey, "Geofence position budget", m.EffectiveGeoFencePositionBudget,
			m.Tier.GeoFencePositionBudget(), m.OverrideGeoFencePositionBudget()),
		// No override level: the count is set on the tier only, so override is always nil.
		row(iam.CalculatedMetricsPerProfileConfigKey, "Calculated metrics per profile",
			m.EffectiveCalculatedMetricsPerProfile, m.Tier.CalculatedMetricsPerProfile(), nil),
		row(iam.IntegrationsPerTenantConfigKey, "Integrations per tenant",
			m.EffectiveIntegrationsPerTenant, m.Tier.IntegrationsPerTenant(), nil),
	}
}
