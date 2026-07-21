// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShedPriorityTierConfigValidation holds the shedPriority tier key to the 1–100
// band scale at the write path — the whole reason the registry rejects at write
// (ADR-065 decision 8) rather than letting a bad value silently inherit.
func TestShedPriorityTierConfigValidation(t *testing.T) {
	for _, v := range []float64{1, 30, 50, 100} {
		require.NoError(t, ValidateTierConfig(map[string]any{"shedPriority": v}),
			"%v is a valid band value", v)
	}
	for _, v := range []any{float64(0), float64(101), float64(-5), float64(50.5), "gold", true} {
		require.Error(t, ValidateTierConfig(map[string]any{"shedPriority": v}),
			"%v (%T) is not a valid band value", v, v)
	}
}

// TestTierShedPriorityReader pins the tier reads its own shedPriority back through the
// same validator the write path uses — an out-of-band junk value inherits (nil)
// rather than banding to a wrong class.
func TestTierShedPriorityReader(t *testing.T) {
	require.Nil(t, (*TenantTier)(nil).ShedPriority(), "a nil tier reads nil")
	require.Nil(t, (&TenantTier{}).ShedPriority(), "a tier with no config reads nil")
	require.Nil(t, (&TenantTier{Config: map[string]any{}}).ShedPriority(), "no key reads nil")

	got := (&TenantTier{Config: map[string]any{"shedPriority": float64(90)}}).ShedPriority()
	require.NotNil(t, got)
	require.Equal(t, 90, *got)

	// An out-of-band junk value (rejected by the API, but a direct DB write could park
	// it) reads as nil (inherit) rather than a wrong band.
	require.Nil(t, (&TenantTier{Config: map[string]any{"shedPriority": float64(0)}}).ShedPriority())
	require.Nil(t, (&TenantTier{Config: map[string]any{"shedPriority": float64(200)}}).ShedPriority())
}

// TestEffectiveShedPriorityCascade pins the ADR-063 cascade: override → tier → nil
// (platform fail-safe), each reporting the level that produced it (decision 7).
func TestEffectiveShedPriorityCascade(t *testing.T) {
	gold := &TenantTier{Token: TierGoldToken, Config: map[string]any{"shedPriority": float64(90)}}

	t.Run("override wins and reports itself", func(t *testing.T) {
		tenant := &Tenant{Tier: gold, ShedPriority: inp(10)}
		v, src := tenant.EffectiveShedPriority()
		require.NotNil(t, v)
		require.Equal(t, 10, *v)
		require.Equal(t, SourceOverride, src)
	})

	t.Run("no override falls to the tier", func(t *testing.T) {
		tenant := &Tenant{Tier: gold}
		v, src := tenant.EffectiveShedPriority()
		require.NotNil(t, v)
		require.Equal(t, 90, *v)
		require.Equal(t, SourceTier, src)
	})

	t.Run("neither declares one, so the platform fail-safe applies", func(t *testing.T) {
		tenant := &Tenant{Tier: &TenantTier{Token: "silver"}}
		v, src := tenant.EffectiveShedPriority()
		require.Nil(t, v, "nil signals the reader substitutes the platform fail-safe, never gold")
		require.Equal(t, SourcePlatformDefault, src)
	})

	t.Run("an unusable override falls THROUGH to the tier, not past it", func(t *testing.T) {
		// The write path rejects this, but a direct DB write could park it. It must
		// fall to the tier's value (decision 5's next level), not skip to the fail-safe
		// — a gold tenant with a junk override must still ride the gold tier.
		tenant := &Tenant{Tier: gold, ShedPriority: inp(0)}
		v, src := tenant.EffectiveShedPriority()
		require.NotNil(t, v)
		require.Equal(t, 90, *v)
		require.Equal(t, SourceTier, src)
	})
}
