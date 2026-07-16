// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/stretchr/testify/require"
)

func fp(v float64) *float64 { return &v }
func inp(v int) *int        { return &v }

// TestEveryDimensionHasOverrideColumns is the fail-loud half of overridesFor.
//
// The dimension → override-column mapping cannot be derived: the overrides are typed
// struct fields, so something must connect "the ingest dimension" to
// IngestMessagesPerSecond. What CAN be guaranteed is that a dimension without one is
// caught here rather than shipped — because the silent failure is invisible on every
// screen: a fourth dimension declared without columns would report every tenant as
// "no override declared", the console would show it as inherited, and per-tenant
// overrides for it would do nothing at all.
//
// If this fails, you declared a governance dimension without adding its override
// columns to Tenant and its case to overridesFor. That is the fix — not deleting the
// assertion.
func TestEveryDimensionHasOverrideColumns(t *testing.T) {
	dims := governance.AllDimensions()
	require.NotEmpty(t, dims)

	var tenant Tenant
	for _, d := range dims {
		_, _, ok := tenant.overridesFor(d)
		require.True(t, ok,
			"governance dimension %q has no override columns on iam.Tenant: add them and a case to overridesFor", d.Name)
	}
}

// TestEveryDimensionCarriesDisplayMetadata pins that a dimension can be RENDERED.
//
// The console builds its tier editor and its effective-settings table by enumerating
// the dimensions, so one declared without a Label or RateUnit does not fail — it
// draws a blank heading and a unitless number on an operator's screen. Both are
// declaration-site facts, so the declaration site is where the omission must hurt.
func TestEveryDimensionCarriesDisplayMetadata(t *testing.T) {
	for _, d := range governance.AllDimensions() {
		require.NotEmpty(t, d.Label, "dimension %q needs an operator-facing Label", d.Name)
		require.NotEmpty(t, d.RateUnit, "dimension %q needs a RateUnit for its rate", d.Name)
	}
}

// TestEffectiveRateProvenance pins that a value never travels without the reason it
// won (ADR-065 decision 7). The VALUES are already covered at the wire seam; what
// matters here is that the level reported is the level that actually produced it —
// an operator reading "tier" beside a number the override produced would be told a
// lie about their own platform.
func TestEffectiveRateProvenance(t *testing.T) {
	gold := &TenantTier{Token: TierGoldToken, Config: map[string]any{
		"ingestMessagesPerSecond": float64(2000),
		"ingestBurst":             float64(4000),
	}}

	t.Run("an override reports itself as the exception it is", func(t *testing.T) {
		tenant := &Tenant{Tier: gold, IngestMessagesPerSecond: fp(5000), IngestBurst: inp(9000)}
		rate, src := tenant.EffectiveRate(governance.Ingest)
		require.Equal(t, float64(5000), *rate)
		require.Equal(t, SourceOverride, src)

		burst, src := tenant.EffectiveBurst(governance.Ingest)
		require.Equal(t, 9000, *burst)
		require.Equal(t, SourceOverride, src)
	})

	t.Run("the tier reports itself when the tenant declares nothing", func(t *testing.T) {
		tenant := &Tenant{Tier: gold}
		rate, src := tenant.EffectiveRate(governance.Ingest)
		require.Equal(t, float64(2000), *rate)
		require.Equal(t, SourceTier, src)

		burst, src := tenant.EffectiveBurst(governance.Ingest)
		require.Equal(t, 4000, *burst)
		require.Equal(t, SourceTier, src)
	})

	t.Run("neither level declaring one is a NULL value and a named source", func(t *testing.T) {
		// The seeded SILVER shape. The value is nil rather than a number on purpose:
		// the platform default is the enforcing service's, not this service's, so the
		// only honest answer is the SOURCE. Anything else would be a third copy of a
		// number user-management does not own.
		tenant := &Tenant{Tier: &TenantTier{Token: TierSilverToken}}
		rate, src := tenant.EffectiveRate(governance.Ingest)
		require.Nil(t, rate)
		require.Equal(t, SourcePlatformDefault, src)

		burst, src := tenant.EffectiveBurst(governance.Ingest)
		require.Nil(t, burst)
		require.Equal(t, SourcePlatformDefault, src)
	})

	t.Run("an unusable override falls through to the tier, not past it", func(t *testing.T) {
		// Only reachable by an out-of-band DB write. It must not report itself as an
		// override — the operator would go looking for a -5 they cannot see anywhere,
		// and the tenant is in fact metered at its tier.
		tenant := &Tenant{Tier: gold, IngestMessagesPerSecond: fp(-5), IngestBurst: inp(0)}
		rate, src := tenant.EffectiveRate(governance.Ingest)
		require.Equal(t, float64(2000), *rate)
		require.Equal(t, SourceTier, src)

		burst, src := tenant.EffectiveBurst(governance.Ingest)
		require.Equal(t, 4000, *burst)
		require.Equal(t, SourceTier, src)

		// And it is not reported as a delta either: an unusable value is not an
		// override the operator set, it is garbage the API cannot produce.
		require.Nil(t, tenant.OverrideRate(governance.Ingest))
		require.Nil(t, tenant.OverrideBurst(governance.Ingest))
	})

	t.Run("a tenant loaded without its tier degrades, never panics", func(t *testing.T) {
		// The tier is a required FK and every read path preloads it, so nil is a bug
		// in a read path. It must cost a tier's tuning, never a ceiling — and never
		// take down the query every enforcing service refreshes against.
		tenant := &Tenant{Tier: nil, IngestMessagesPerSecond: fp(750)}
		rate, src := tenant.EffectiveRate(governance.Ingest)
		require.Equal(t, float64(750), *rate)
		require.Equal(t, SourceOverride, src)

		bare := &Tenant{Tier: nil}
		rate, src = bare.EffectiveRate(governance.Ingest)
		require.Nil(t, rate)
		require.Equal(t, SourcePlatformDefault, src)
	})
}

// TestUnmappedDimensionReportsNoOverride pins the fail-safe direction of a dimension
// with no override columns — the state TestEveryDimensionHasOverrideColumns exists to
// prevent, pinned here so its behavior is a decision rather than an accident. It must
// read as "no override" (and so resolve to the tier or the platform default), never
// as a ceiling of zero, which would admit nothing.
func TestUnmappedDimensionReportsNoOverride(t *testing.T) {
	unknown := governance.Dimension{
		Name: "not-a-real-dimension", RateField: "nope", BurstField: "nopeBurst", PerSecondScale: 1,
	}
	tenant := &Tenant{Tier: &TenantTier{Token: TierGoldToken}, IngestMessagesPerSecond: fp(5000)}

	require.Nil(t, tenant.OverrideRate(unknown))
	require.Nil(t, tenant.OverrideBurst(unknown))

	rate, src := tenant.EffectiveRate(unknown)
	require.Nil(t, rate)
	require.Equal(t, SourcePlatformDefault, src)
}
