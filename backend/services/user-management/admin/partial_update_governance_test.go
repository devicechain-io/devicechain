// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// 🔴 THE GOVERNANCE HALF OF THE CONVERSION, WHICH IS A CHANGE TO WHAT nil MEANS AND
// THEREFORE NEEDS EVIDENCE THAT IT STILL MEANS THE SAME THING.
//
// The eleven per-tenant ADR-023 overrides are `*float64` / `*int` on the row, and nil is
// not "unset" — it is the load-bearing statement "this tenant declares no override, so
// it inherits its TIER and then the enforcing service's PLATFORM DEFAULT, and NEVER
// unlimited". Under the full-replace shape the request pointer had to carry ABSENT and
// CLEARED at once, so the conversion is a redefinition of a governance value dressed as
// a refactor, and the whole of it has to be shown reaching the row:
//
//	omitted        the override survives unchanged
//	explicit null  the override is REMOVED — and the cascade then resolves the tenant to
//	               its tier, or to the platform default, rather than to ZERO or unlimited
//	a value        the override is set
//
// The harness drives all three onto the COLUMN. What it cannot see is the last clause,
// because "the column is NULL" and "a removed override resolves to the platform default"
// are two different claims and only the first is a row. iam's cascade is what turns one
// into the other, and this is where the two are joined.
func TestClearingAnOverrideResolvesToTheTierThenThePlatformDefault(t *testing.T) {
	s := newPartialUpdateService(t, &iam.TenantTier{}, &iam.Tenant{})
	ctx := putest.TenantContext(partialUpdateTenant)()

	// A tier that declares a ceiling of its own, so "fell back to the tier" and "fell all
	// the way through" are distinguishable. Without a tier value, a cleared override would
	// resolve to the platform default either way and the middle rung of the cascade would
	// be unmeasured.
	tierConfig := `{"ingestMessagesPerSecond":2000}`
	cfg, err := ParseConfigJSON(&tierConfig)
	require.NoError(t, err)
	_, err = s.CreateTenantTier(ctx, TierInput{Token: "gold-fixture", Config: cfg})
	require.NoError(t, err)

	_, err = s.CreateTenant(ctx, TenantInput{
		Token: "acme-co", TierToken: "gold-fixture",
		GovernanceOverrides: GovernanceOverrides{
			IngestMessagesPerSecond: fptr(50),
			IngestBurst:             iptr(60),
			ShedPriority:            iptr(70),
		},
	})
	require.NoError(t, err)

	reload := func() *iam.Tenant {
		tn, err := s.iam.TenantByToken(ctx, "acme-co")
		require.NoError(t, err)
		require.NotNil(t, tn.Tier, "the cascade reads through the preloaded tier")
		return tn
	}

	// The premise: while the override stands, it is what the cascade reports.
	rate, source := reload().EffectiveRate(governance.Ingest)
	require.NotNil(t, rate)
	require.Equal(t, float64(50), *rate)
	require.Equal(t, iam.SourceOverride, source)

	// An unrelated edit — a rename — must not touch any of it. This is the defect the
	// conversion removes, stated at the level an operator experiences it: renaming a
	// tenant used to drop it from its bespoke ceiling to whatever its tier sold.
	_, err = s.UpdateTenant(ctx, "acme-co", &TenantUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Acme Industrial"),
	})
	require.NoError(t, err)
	rate, source = reload().EffectiveRate(governance.Ingest)
	require.NotNil(t, rate, "renaming a tenant removed its ingest override")
	require.Equal(t, float64(50), *rate)
	require.Equal(t, iam.SourceOverride, source)

	// 🔴 THE CLEAR, AND WHERE IT LANDS. An explicit null removes the override, and the
	// tenant then resolves to ITS TIER — 2000/s, not 0/s, and not "unlimited".
	_, err = s.UpdateTenant(ctx, "acme-co", &TenantUpdateRequest{
		IngestMessagesPerSecond: dcgraphql.ClearedFloat64(),
	})
	require.NoError(t, err)
	after := reload()
	require.Nil(t, after.IngestMessagesPerSecond,
		"the column still holds a value, so the clear did not reach storage")
	rate, source = after.EffectiveRate(governance.Ingest)
	require.NotNil(t, rate, "a removed override resolved to nothing rather than to the tier")
	require.Equal(t, float64(2000), *rate,
		"a removed override must fall back to the tier's ceiling, not to zero")
	require.Equal(t, iam.SourceTier, source)

	// The burst has no tier value, so removing its override falls ALL the way through —
	// to SourcePlatformDefault with a nil value, which is this service saying "the
	// enforcing service's own number applies". A nil here is the correct answer and a
	// ZERO would be the dangerous one: a bucket built from zero admits nothing.
	_, err = s.UpdateTenant(ctx, "acme-co", &TenantUpdateRequest{
		IngestBurst: dcgraphql.ClearedInt32(),
	})
	require.NoError(t, err)
	after = reload()
	require.Nil(t, after.IngestBurst)
	burst, source := after.EffectiveBurst(governance.Ingest)
	require.Nil(t, burst,
		"a removed burst override resolved to a NUMBER this service does not own — the "+
			"platform default belongs to the enforcing service, and stating one here would "+
			"be a third copy of it")
	require.Equal(t, iam.SourcePlatformDefault, source)

	// And the same for a scalar preference rather than a ceiling: shedPriority's
	// fail-safe is a bronze-band value the enforcing service substitutes, so a removed
	// override must arrive as nil/platform-default and never as 0 — which names no band
	// at all and would make the tenant unclassifiable.
	_, err = s.UpdateTenant(ctx, "acme-co", &TenantUpdateRequest{
		ShedPriority: dcgraphql.ClearedInt32(),
	})
	require.NoError(t, err)
	after = reload()
	require.Nil(t, after.ShedPriority)
	prio, source := after.EffectiveShedPriority()
	require.Nil(t, prio)
	require.Equal(t, iam.SourcePlatformDefault, source)
}

// TestAZeroOverrideIsStillRefusedOnUpdate is the counterweight: "null removes the
// override" must not have become "any falsy value removes it".
//
// Zero is refused rather than folded to nil, which is the rule the create path has always
// applied — an override of 0 is not "inherit", it is a bucket that admits nothing, and
// accepting it would be an outage for the tenant that reported as a successful edit.
// Validation runs on the FOLDED set, so this also pins that the fold happens before the
// check rather than after it.
func TestAZeroOverrideIsStillRefusedOnUpdate(t *testing.T) {
	s := newPartialUpdateService(t, &iam.TenantTier{}, &iam.Tenant{})
	ctx := putest.TenantContext(partialUpdateTenant)()

	_, err := s.CreateTenantTier(ctx, TierInput{Token: "gold-fixture"})
	require.NoError(t, err)
	_, err = s.CreateTenant(ctx, TenantInput{
		Token: "acme-co", TierToken: "gold-fixture",
		GovernanceOverrides: GovernanceOverrides{IngestMessagesPerSecond: fptr(50)},
	})
	require.NoError(t, err)

	_, err = s.UpdateTenant(ctx, "acme-co", &TenantUpdateRequest{
		IngestMessagesPerSecond: dcgraphql.OptionalFloat64Of(0),
	})
	require.Error(t, err, "a zero ingest ceiling was accepted, which meters the tenant at nothing")
	require.Contains(t, err.Error(), "must be positive")

	// And the refusal wrote nothing.
	tn, err := s.iam.TenantByToken(ctx, "acme-co")
	require.NoError(t, err)
	require.NotNil(t, tn.IngestMessagesPerSecond)
	require.Equal(t, float64(50), *tn.IngestMessagesPerSecond)
}
