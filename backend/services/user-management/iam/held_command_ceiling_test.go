// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateTenantRoundTripsHeldCommandCeiling pins the write-side invariant for the
// new override, the same one TestUpdateTenantRoundTripsAiGovernance pins for the AI
// ceilings. UpdateTenant writes through a column ALLOWLIST rather than a full-row Save,
// and a column missing from that Select is silently unwritable: the update reports
// success, the operator sees their number echoed back by the resolver they just wrote
// through, and the old value survives in the database. For a ceiling that means a bound
// an operator tightened after an incident never actually takes effect.
//
// It covers all three transitions, because they fail differently: set (the column is in
// the list at all), raise (a second write is not ignored), and clear-to-inherit (a nil
// pointer writes NULL rather than being skipped as a zero value).
func TestUpdateTenantRoundTripsHeldCommandCeiling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme"}))

	reload := func() *Tenant {
		got, err := s.TenantByToken(ctx, "acme")
		require.NoError(t, err)
		return got
	}

	require.Nil(t, reload().HeldCommandCeiling, "a freshly created tenant inherits")

	tt := reload()
	tt.HeldCommandCeiling = inp(2500)
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.NotNil(t, reload().HeldCommandCeiling,
		"heldCommandCeiling is missing from UpdateTenant's Select allowlist — the write is silently dropped")
	require.Equal(t, 2500, *reload().HeldCommandCeiling)

	// Tightening it must take effect. This is the transition an operator makes after an
	// offline fleet fills the queue, and the one where a silent no-op is most expensive.
	tt = reload()
	tt.HeldCommandCeiling = inp(500)
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.Equal(t, 500, *reload().HeldCommandCeiling)

	// Clearing reverts to inherit (the tier, then the service default) — NULL, not 0.
	tt = reload()
	tt.HeldCommandCeiling = nil
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.Nil(t, reload().HeldCommandCeiling,
		"clearing the override must write NULL (inherit), not leave the old bound in place")
}

// TestHeldCommandCeilingTierConfigValidation holds the heldCommandCeiling tier key to
// the "positive whole number" rule at the write path — the whole reason the registry
// rejects at write (ADR-065 decision 8) rather than letting a bad value silently
// inherit while an operator believes they sold a bound.
func TestHeldCommandCeilingTierConfigValidation(t *testing.T) {
	// A ceiling has no upper bound in its MEANING, only in its representation, which is
	// the difference from shedPriority: 100_000 is an ordinary fleet backlog, not an
	// out-of-band value.
	for _, v := range []any{float64(1), float64(500), float64(100_000), 4000} {
		require.NoError(t, ValidateTierConfig(map[string]any{"heldCommandCeiling": v}),
			"%v (%T) is a valid ceiling", v, v)
	}
	for _, v := range []any{
		float64(0),                 // "hold nothing" is an outage, not a default
		float64(-5),                // likewise
		float64(1.5),               // a count is not fractional
		float64(math.MaxInt32) * 2, // does not survive the GraphQL Int it round-trips through
		math.Inf(1), math.NaN(),
		"lots", true,
	} {
		require.Error(t, ValidateTierConfig(map[string]any{"heldCommandCeiling": v}),
			"%v (%T) is not a valid ceiling", v, v)
	}
}

// TestTierHeldCommandCeilingReader pins that the tier reads its own heldCommandCeiling
// back through the same validator the write path uses — an out-of-band junk value
// inherits (nil) rather than becoming a live bound of zero, which would refuse every
// command to an absent device for every tenant at the tier.
func TestTierHeldCommandCeilingReader(t *testing.T) {
	require.Nil(t, (*TenantTier)(nil).HeldCommandCeiling(), "a nil tier reads nil")
	require.Nil(t, (&TenantTier{}).HeldCommandCeiling(), "a tier with no config reads nil")
	require.Nil(t, (&TenantTier{Config: map[string]any{}}).HeldCommandCeiling(), "no key reads nil")

	got := (&TenantTier{Config: map[string]any{"heldCommandCeiling": float64(5000)}}).HeldCommandCeiling()
	require.NotNil(t, got)
	require.Equal(t, 5000, *got)

	require.Nil(t, (&TenantTier{Config: map[string]any{"heldCommandCeiling": float64(0)}}).HeldCommandCeiling())
	require.Nil(t, (&TenantTier{Config: map[string]any{"heldCommandCeiling": "unlimited"}}).HeldCommandCeiling(),
		`there is no spelling of "unlimited" — a junk value inherits`)
}

// TestEffectiveHeldCommandCeilingCascade pins the ADR-065 decision 5 cascade for the
// HELD-command ceiling: override → tier → nil (the enforcing service's own default),
// each reporting the level that produced it (decision 7).
func TestEffectiveHeldCommandCeilingCascade(t *testing.T) {
	gold := &TenantTier{Token: TierGoldToken, Config: map[string]any{"heldCommandCeiling": float64(50_000)}}

	t.Run("override wins and reports itself", func(t *testing.T) {
		tenant := &Tenant{Tier: gold, HeldCommandCeiling: inp(200)}
		v, src := tenant.EffectiveHeldCommandCeiling()
		require.NotNil(t, v)
		require.Equal(t, 200, *v)
		require.Equal(t, SourceOverride, src)
	})

	t.Run("no override falls to the tier", func(t *testing.T) {
		tenant := &Tenant{Tier: gold}
		v, src := tenant.EffectiveHeldCommandCeiling()
		require.NotNil(t, v)
		require.Equal(t, 50_000, *v)
		require.Equal(t, SourceTier, src)
	})

	t.Run("neither declares one, so the service's own default applies", func(t *testing.T) {
		tenant := &Tenant{Tier: &TenantTier{Token: "silver"}}
		v, src := tenant.EffectiveHeldCommandCeiling()
		require.Nil(t, v, "nil signals the reader substitutes its configured default — a real bound, never unlimited")
		require.Equal(t, SourcePlatformDefault, src)
	})

	t.Run("an unusable override falls THROUGH to the tier, not past it", func(t *testing.T) {
		// The write path rejects this, but a direct DB write could park it. It must fall
		// to the tier's value (decision 5's next level), not skip to the service default
		// — a gold tenant with a junk override must still get the gold tier's headroom.
		tenant := &Tenant{Tier: gold, HeldCommandCeiling: inp(0)}
		v, src := tenant.EffectiveHeldCommandCeiling()
		require.NotNil(t, v)
		require.Equal(t, 50_000, *v)
		require.Equal(t, SourceTier, src)
	})

	t.Run("a tenant with no tier loaded still resolves, in the fail-safe direction", func(t *testing.T) {
		// The tier is a required FK and every read path preloads it, so a nil Tier is a
		// caller bug. It must cost a tier's tuning, not deref-panic the query every
		// enforcing service refreshes against.
		v, src := (&Tenant{}).EffectiveHeldCommandCeiling()
		require.Nil(t, v)
		require.Equal(t, SourcePlatformDefault, src)
	})
}

// TestUsableHeldCommandCeilingIsTheCeilingRuleNotTheBandRule is the counterweight to the
// cascade above: it pins WHICH rule decides usability. Shaping this on the shed-priority
// band (1–100) would be the plausible mistake — both are ints carried on a tier — and it
// would silently reject every realistic fleet backlog as unusable, falling every such
// tenant through to its tier while the override sat visibly in the API.
func TestUsableHeldCommandCeilingIsTheCeilingRuleNotTheBandRule(t *testing.T) {
	require.True(t, UsableHeldCommandCeiling(1))
	require.True(t, UsableHeldCommandCeiling(100))
	require.True(t, UsableHeldCommandCeiling(100_000),
		"a ceiling above the shed band's 100 must still be usable — it is a count, not a point on a scale")
	require.True(t, UsableHeldCommandCeiling(math.MaxInt32))

	require.False(t, UsableHeldCommandCeiling(0), "zero is not a bound, it is a refusal")
	require.False(t, UsableHeldCommandCeiling(-1))
	require.False(t, UsableHeldCommandCeiling(math.MaxInt32+1),
		"a value that cannot survive the GraphQL Int it round-trips through is not usable")
}
