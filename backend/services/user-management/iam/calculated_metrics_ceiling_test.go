// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/governance"
)

// TestTheCalculatedMetricsCeilingValidator pins both walls with values derived from the
// platform constant, and the one property this key has that no other tier ceiling does:
// zero is accepted, because zero means "off" and is also the platform default.
func TestTheCalculatedMetricsCeilingValidator(t *testing.T) {
	key := CalculatedMetricsPerProfileConfigKey
	for _, ok := range []any{0, float64(0), 1, float64(8), governance.MaxCalculatedMetricsPerProfile} {
		require.NoErrorf(t, ValidateTierConfig(map[string]any{key: ok}), "%v must be a legal tier value", ok)
	}
	for _, bad := range []any{-1, float64(governance.MaxCalculatedMetricsPerProfile + 1), 1.5, "8", true} {
		require.Errorf(t, ValidateTierConfig(map[string]any{key: bad}), "%v must be refused", bad)
	}
}

func TestTheCalculatedMetricsCeilingCascade(t *testing.T) {
	tier := func(v any) *TenantTier {
		return &TenantTier{Token: TierGoldToken, Config: map[string]any{CalculatedMetricsPerProfileConfigKey: v}}
	}

	v, src := (&Tenant{Tier: tier(float64(8))}).EffectiveCalculatedMetricsPerProfile()
	require.Equal(t, SourceTier, src)
	require.Equal(t, 8, *v)

	// An explicit zero on the tier is a real setting — off — not "inherit".
	v, src = (&Tenant{Tier: tier(float64(0))}).EffectiveCalculatedMetricsPerProfile()
	require.Equal(t, SourceTier, src)
	require.Equal(t, 0, *v)

	// Nothing declared: the platform default, which the reader folds onto zero.
	v, src = (&Tenant{Tier: &TenantTier{Token: TierSilverToken}}).EffectiveCalculatedMetricsPerProfile()
	require.Equal(t, SourcePlatformDefault, src)
	require.Nil(t, v)

	// A junk value parked out of band inherits rather than becoming a ceiling.
	v, src = (&Tenant{Tier: tier(float64(500))}).EffectiveCalculatedMetricsPerProfile()
	require.Equal(t, SourcePlatformDefault, src)
	require.Nil(t, v)
}
