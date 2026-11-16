// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRulesPerTenantTierCascade(t *testing.T) {
	for _, value := range []any{0, float64(3), math.MaxInt32} {
		require.NoError(t, ValidateTierConfig(map[string]any{"rulesPerTenant": value}))
		tenant := &Tenant{Tier: &TenantTier{Config: map[string]any{"rulesPerTenant": value}}}
		got, source := tenant.EffectiveRulesPerTenant()
		require.NotNil(t, got)
		f, _ := toFloat(value)
		require.Equal(t, int(f), *got)
		require.Equal(t, SourceTier, source)
	}
	for _, value := range []any{-1, 1.5, "3", true, math.Inf(1), math.NaN(), float64(math.MaxInt32) + 1} {
		require.Error(t, ValidateTierConfig(map[string]any{"rulesPerTenant": value}))
		tenant := &Tenant{Tier: &TenantTier{Config: map[string]any{"rulesPerTenant": value}}}
		got, source := tenant.EffectiveRulesPerTenant()
		require.Nil(t, got)
		require.Equal(t, SourcePlatformDefault, source)
	}
	got, source := (&Tenant{}).EffectiveRulesPerTenant()
	require.Nil(t, got)
	require.Equal(t, SourcePlatformDefault, source)
}
