// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/governance"
)

// geoFenceCapKeys is the table every test below walks: the three keys, the accessor that reads
// each off a tier, the tenant column each overrides, and the platform maximum each is bounded
// by. Written once because three near-identical keys is precisely the shape in which a test
// covers one of them three times.
var geoFenceCapKeys = []struct {
	name     string
	key      string
	max      int
	tierRead func(*TenantTier) *int
	column   func(*Tenant) **int
	usable   func(int) bool
	eff      func(*Tenant) (*int, SettingSource)
}{
	{
		name: "geoFenceVertexCeiling", key: GeoFenceVertexCeilingConfigKey,
		max:      governance.MaxGeoFenceVertexCeiling,
		tierRead: (*TenantTier).GeoFenceVertexCeiling,
		column:   func(t *Tenant) **int { return &t.GeoFenceVertexCeiling },
		usable:   UsableGeoFenceVertexCeiling,
		eff:      (*Tenant).EffectiveGeoFenceVertexCeiling,
	},
	{
		name: "geoFenceCeiling", key: GeoFenceCeilingConfigKey,
		max:      governance.MaxGeoFenceCeiling,
		tierRead: (*TenantTier).GeoFenceCeiling,
		column:   func(t *Tenant) **int { return &t.GeoFenceCeiling },
		usable:   UsableGeoFenceCeiling,
		eff:      (*Tenant).EffectiveGeoFenceCeiling,
	},
	{
		name: "geoFenceVertexBudget", key: GeoFenceVertexBudgetConfigKey,
		max:      governance.MaxTenantGeometryVertices,
		tierRead: (*TenantTier).GeoFenceVertexBudget,
		column:   func(t *Tenant) **int { return &t.GeoFenceVertexBudget },
		usable:   UsableGeoFenceVertexBudget,
		eff:      (*Tenant).EffectiveGeoFenceVertexBudget,
	},
}

// TestTheGeoFenceCapsAreRegisteredTierKeys pins that all three are in the registry. An
// unregistered key is REJECTED at write with "unknown tier setting", so a missing registration
// is loud rather than silent — but it is loud in a place an operator meets and a test does not,
// and it would make the whole feature unreachable from a tier.
func TestTheGeoFenceCapsAreRegisteredTierKeys(t *testing.T) {
	registered := make(map[string]bool)
	for _, k := range TierConfigKeys() {
		registered[k] = true
	}
	for _, c := range geoFenceCapKeys {
		require.Truef(t, registered[c.key], "%s is not a registered tier key, so no tier can carry it", c.key)
	}
}

// TestTheGeoFenceCapValidatorsEnforceTheirMaximum is the reason these keys exist as their own
// shape rather than reusing validateHeldCommandCeiling. Every other tier ceiling bounds only its
// REPRESENTATION; these bound a resource spent in a process every tenant shares, so a maximum is
// a platform property, not a suggestion.
//
// 🔴 IT PINS BOTH SIDES OF EACH WALL WITH LITERALS DERIVED FROM THE CONSTANT — max accepted,
// max+1 rejected. A case written only as "reject something huge" would keep passing if the
// maximum were raised tenfold, which is exactly the edit this test exists to notice.
func TestTheGeoFenceCapValidatorsEnforceTheirMaximum(t *testing.T) {
	for _, c := range geoFenceCapKeys {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, ValidateTierConfig(map[string]any{c.key: c.max}),
				"the maximum itself must be a legal tier value")
			require.NoError(t, ValidateTierConfig(map[string]any{c.key: 1}),
				"one is a legal (if useless) cap")
			require.Error(t, ValidateTierConfig(map[string]any{c.key: c.max + 1}),
				"a value one past the maximum must be refused, not clamped")
			require.Error(t, ValidateTierConfig(map[string]any{c.key: 10 * c.max}),
				"a value far past the maximum must be refused")

			// The rules it inherits from the burst validator, which must not have been lost
			// by wrapping it: zero and negative are not caps, and a fraction is not a count.
			for _, bad := range []any{0, -1, 1.5, "512"} {
				require.Errorf(t, ValidateTierConfig(map[string]any{c.key: bad}),
					"%v must not be a legal value for %s", bad, c.key)
			}

			// The error has to name the number, or an operator who has just been refused
			// cannot tell whether to type something smaller or to raise a platform constant.
			err := ValidateTierConfig(map[string]any{c.key: c.max + 1})
			require.Containsf(t, err.Error(), c.key, "the error does not name the key: %v", err)
		})
	}
}

// TestAGeoFenceCapAboveTheMaximumFallsThroughRatherThanApplying pins the READ side of the same
// wall. Both write doors reject an over-large value, so one can only arrive by a direct
// out-of-band database write — which is precisely the case a defensive read is for. Inheriting
// is the safe reading of a value that should not exist; honouring it would let a row edit
// escalate a tenant past a platform bound.
func TestAGeoFenceCapAboveTheMaximumFallsThroughRatherThanApplying(t *testing.T) {
	for _, c := range geoFenceCapKeys {
		t.Run(c.name, func(t *testing.T) {
			require.False(t, c.usable(c.max+1), "a value past the maximum must not be a usable override")
			require.True(t, c.usable(c.max), "the maximum itself must be a usable override")

			// Through the tier: an over-large config value reads back as nil (inherit).
			over := &TenantTier{Config: map[string]any{c.key: c.max + 1}}
			require.Nil(t, c.tierRead(over), "an over-large tier value must read back as inherit")
			ok := &TenantTier{Config: map[string]any{c.key: c.max}}
			require.NotNil(t, c.tierRead(ok), "the maximum must read back as a live tier value")

			// Through the tenant column: an over-large override falls through to the TIER,
			// not past it to the platform default. The distinction is ADR-065 D5's, and
			// getting it wrong here would silently un-tier the tenant.
			tenant := &Tenant{Tier: &TenantTier{Config: map[string]any{c.key: 7}}}
			*c.column(tenant) = inp(c.max + 1)
			v, src := c.eff(tenant)
			require.NotNil(t, v)
			require.Equal(t, 7, *v, "an unusable override must fall through to the tier")
			require.Equal(t, SourceTier, src)
		})
	}
}

// TestTheGeoFenceCapsCascadeIndependently is the decision this shape encodes, stated as a test:
// a tenant that overrides ONE cap keeps inheriting the other two. Grouping them would mean an
// operator raising one silently reset the others to the platform default — the same full-replace
// hazard every admin override guards against by being individually readable.
func TestTheGeoFenceCapsCascadeIndependently(t *testing.T) {
	for _, c := range geoFenceCapKeys {
		t.Run("override "+c.name, func(t *testing.T) {
			tenant := &Tenant{Tier: &TenantTier{}}
			*c.column(tenant) = inp(11)

			for _, other := range geoFenceCapKeys {
				v, src := other.eff(tenant)
				if other.key == c.key {
					require.NotNil(t, v)
					require.Equal(t, 11, *v)
					require.Equal(t, SourceOverride, src)
					continue
				}
				require.Nilf(t, v, "overriding %s also set %s", c.name, other.name)
				require.Equalf(t, SourcePlatformDefault, src,
					"overriding %s changed where %s resolves from", c.name, other.name)
			}
		})
	}
}

// TestTheGeoFenceCapCascadeProvenance walks the full ADR-065 ladder for each key: override beats
// tier beats nothing, and "nothing" is SourcePlatformDefault with a NIL value — inherit, never a
// number this package invented.
func TestTheGeoFenceCapCascadeProvenance(t *testing.T) {
	for _, c := range geoFenceCapKeys {
		t.Run(c.name, func(t *testing.T) {
			bare := &Tenant{}
			v, src := c.eff(bare)
			require.Nil(t, v, "a tenant with no tier and no override must resolve to nil (inherit)")
			require.Equal(t, SourcePlatformDefault, src)

			tiered := &Tenant{Tier: &TenantTier{Config: map[string]any{c.key: 42}}}
			v, src = c.eff(tiered)
			require.NotNil(t, v)
			require.Equal(t, 42, *v)
			require.Equal(t, SourceTier, src)

			*c.column(tiered) = inp(9)
			v, src = c.eff(tiered)
			require.NotNil(t, v)
			require.Equal(t, 9, *v, "an override must beat the tier")
			require.Equal(t, SourceOverride, src)
		})
	}
}

// TestUpdateTenantRoundTripsTheGeoFenceCaps pins the write-side invariant, the same one
// TestUpdateTenantRoundTripsHeldCommandCeiling pins for the HELD ceiling. UpdateTenant writes
// through a column ALLOWLIST rather than a full-row Save, and a column missing from that Select
// is silently unwritable: the update reports success, the operator sees their number echoed back
// by the resolver they wrote through, and the old value survives in the database.
//
// 🔴 IT WRITES ALL THREE AT ONCE WITH DISTINCT VALUES, then reads each back by value. Three
// same-typed nullable ints added to one allowlist in one edit is where a copy-pasted column name
// lands two entries on the same field — and every "is it set?" assertion would pass.
func TestUpdateTenantRoundTripsTheGeoFenceCaps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "fenced"}))

	reload := func() *Tenant {
		got, err := s.TenantByToken(ctx, "fenced")
		require.NoError(t, err)
		return got
	}

	for _, c := range geoFenceCapKeys {
		require.Nilf(t, *c.column(reload()), "a freshly created tenant inherits %s", c.name)
	}

	tt := reload()
	for i, c := range geoFenceCapKeys {
		*c.column(tt) = inp(100 + i)
	}
	require.NoError(t, s.UpdateTenant(ctx, tt))
	back := reload()
	for i, c := range geoFenceCapKeys {
		got := *c.column(back)
		require.NotNilf(t, got, "%s is missing from UpdateTenant's Select allowlist — the write is silently dropped", c.name)
		require.Equalf(t, 100+i, *got, "%s read back another column's value", c.name)
	}

	// Tightening must take effect. This is the transition an operator makes after a tenant
	// fills the shared geometry cache, and the one where a silent no-op is most expensive.
	tt = reload()
	for _, c := range geoFenceCapKeys {
		*c.column(tt) = inp(5)
	}
	require.NoError(t, s.UpdateTenant(ctx, tt))
	for _, c := range geoFenceCapKeys {
		require.Equalf(t, 5, **c.column(reload()), "tightening %s did not take effect", c.name)
	}

	// Clearing reverts to inherit — NULL, not 0. Zero would read as "this tenant may author
	// no fence at all", which is an outage rather than a default.
	tt = reload()
	for _, c := range geoFenceCapKeys {
		*c.column(tt) = nil
	}
	require.NoError(t, s.UpdateTenant(ctx, tt))
	for _, c := range geoFenceCapKeys {
		require.Nilf(t, *c.column(reload()),
			"clearing %s must write NULL (inherit), not leave the old cap in place", c.name)
	}
}
