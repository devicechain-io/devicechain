// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/governance"
)

// TestTheIntegrationsCeilingValidator pins both walls with values derived from the platform
// constant. Zero is accepted: a tier may hold no integrations.
func TestTheIntegrationsCeilingValidator(t *testing.T) {
	key := IntegrationsPerTenantConfigKey
	for _, ok := range []any{0, float64(0), 1, float64(8), governance.MaxIntegrationsPerTenant} {
		require.NoErrorf(t, ValidateTierConfig(map[string]any{key: ok}), "%v must be a legal tier value", ok)
	}
	for _, bad := range []any{-1, float64(governance.MaxIntegrationsPerTenant + 1), 1.5, "8", true} {
		require.Errorf(t, ValidateTierConfig(map[string]any{key: bad}), "%v must be refused", bad)
	}
}

func TestTheIntegrationsCeilingCascade(t *testing.T) {
	tier := func(v any) *TenantTier {
		return &TenantTier{Token: TierGoldToken, Config: map[string]any{IntegrationsPerTenantConfigKey: v}}
	}

	v, src := (&Tenant{Tier: tier(float64(8))}).EffectiveIntegrationsPerTenant()
	require.Equal(t, SourceTier, src)
	require.Equal(t, 8, *v)

	// An explicit zero on the tier is a real setting, not "inherit".
	v, src = (&Tenant{Tier: tier(float64(0))}).EffectiveIntegrationsPerTenant()
	require.Equal(t, SourceTier, src)
	require.Equal(t, 0, *v)

	// Nothing declared: the platform default, which the reader folds on.
	v, src = (&Tenant{Tier: &TenantTier{Token: TierSilverToken}}).EffectiveIntegrationsPerTenant()
	require.Equal(t, SourcePlatformDefault, src)
	require.Nil(t, v)

	// A junk value parked out of band inherits rather than becoming a ceiling.
	v, src = (&Tenant{Tier: tier(float64(500))}).EffectiveIntegrationsPerTenant()
	require.Equal(t, SourcePlatformDefault, src)
	require.Nil(t, v)
}
