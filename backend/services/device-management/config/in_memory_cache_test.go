// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unset, the per-device cache bounds take their defaults; set, they are kept.
func TestInMemoryCacheSettingsLoadAndDefault(t *testing.T) {
	for _, tc := range []struct {
		doc          string
		entries, mib int
	}{
		{`{}`, DefaultPerDeviceCacheEntries, DefaultPerDeviceCacheMiB},
		{`{"inMemoryCache":{"perDeviceCacheEntries":5000}}`, 5000, DefaultPerDeviceCacheMiB},
		{`{"inMemoryCache":{"perDeviceCacheMiB":48}}`, DefaultPerDeviceCacheEntries, 48},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
			assert.Equal(t, tc.entries, cfg.InMemoryCache.PerDeviceCacheEntries)
			assert.Equal(t, tc.mib, cfg.InMemoryCache.PerDeviceCacheMiB)
		})
	}
	// The defaults the documentation and the chart comment state.
	assert.Equal(t, 131072, DefaultPerDeviceCacheEntries)
	assert.Equal(t, 80, DefaultPerDeviceCacheMiB)
}

// A bound outside its range fails the load, naming the key. A negative value is refused,
// not read as unset.
func TestInMemoryCacheSettingsAreBounded(t *testing.T) {
	for _, tc := range []struct{ doc, key string }{
		{`{"inMemoryCache":{"perDeviceCacheEntries":-1}}`, "inMemoryCache.perDeviceCacheEntries"},
		{`{"inMemoryCache":{"perDeviceCacheEntries":4194305}}`, "inMemoryCache.perDeviceCacheEntries"},
		{`{"inMemoryCache":{"perDeviceCacheMiB":-1}}`, "inMemoryCache.perDeviceCacheMiB"},
		{`{"inMemoryCache":{"perDeviceCacheMiB":257}}`, "inMemoryCache.perDeviceCacheMiB"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			err := core.LoadConfiguration([]byte(tc.doc), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
	// The top of each range is accepted.
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration(
		[]byte(`{"inMemoryCache":{"perDeviceCacheEntries":4194304,"perDeviceCacheMiB":256}}`), cfg))
}

// The resolution cache's bytes and per-tenant share default, load, and are bounded.
func TestProfileResolutionCacheSettings(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), cfg))
	assert.Equal(t, 32, cfg.InMemoryCache.ProfileResolutionCacheMiB)
	assert.Equal(t, 25, cfg.InMemoryCache.ProfileResolutionTenantSharePercent)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration(
		[]byte(`{"inMemoryCache":{"profileResolutionCacheMiB":64,"profileResolutionTenantSharePercent":50}}`), cfg))
	assert.Equal(t, 64, cfg.InMemoryCache.ProfileResolutionCacheMiB)
	assert.Equal(t, 50, cfg.InMemoryCache.ProfileResolutionTenantSharePercent)

	for _, tc := range []struct{ doc, key string }{
		{`{"inMemoryCache":{"profileResolutionCacheMiB":-1}}`, "inMemoryCache.profileResolutionCacheMiB"},
		{`{"inMemoryCache":{"profileResolutionCacheMiB":257}}`, "inMemoryCache.profileResolutionCacheMiB"},
		{`{"inMemoryCache":{"profileResolutionTenantSharePercent":101}}`, "inMemoryCache.profileResolutionTenantSharePercent"},
		{`{"inMemoryCache":{"profileResolutionTenantSharePercent":-5}}`, "inMemoryCache.profileResolutionTenantSharePercent"},
	} {
		err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
		require.Error(t, err, tc.doc)
		assert.Contains(t, err.Error(), tc.key)
	}
}

// A tenant's share of the resolution cache must hold at least one maximal payload decoder, or
// every event of a device type with one compiles it again. The boundary is tested on both sides.
func TestProfileResolutionTenantShareMustHoldAMaximalDecoder(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		ok   bool
	}{
		{"the footgun: 16 MiB at 25%", `{"inMemoryCache":{"profileResolutionCacheMiB":16,"profileResolutionTenantSharePercent":25}}`, false},
		{"1 MiB at 100%", `{"inMemoryCache":{"profileResolutionCacheMiB":1,"profileResolutionTenantSharePercent":100}}`, false},
		{"256 MiB at 1%", `{"inMemoryCache":{"profileResolutionCacheMiB":256,"profileResolutionTenantSharePercent":1}}`, false},
		{"16 MiB at 50%", `{"inMemoryCache":{"profileResolutionCacheMiB":16,"profileResolutionTenantSharePercent":50}}`, true},
		{"7 MiB at 100%, 32 bytes short (the cache takes bytes/100 x percent)", `{"inMemoryCache":{"profileResolutionCacheMiB":7,"profileResolutionTenantSharePercent":100}}`, false},
		{"6 MiB at 100%", `{"inMemoryCache":{"profileResolutionCacheMiB":6,"profileResolutionTenantSharePercent":100}}`, false},
		{"8 MiB at 100%", `{"inMemoryCache":{"profileResolutionCacheMiB":8,"profileResolutionTenantSharePercent":100}}`, true},
		{"the defaults", `{}`, true},
	} {
		err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
		if tc.ok {
			require.NoError(t, err, tc.name)
			continue
		}
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), "profileResolutionTenantSharePercent", tc.name)
		assert.Contains(t, err.Error(), "compile it again", tc.name)
	}
}

// The defaults are on the accepted side of the rule with room to spare, so a change to either
// default that breaks the rule fails here rather than at an operator's start.
func TestDefaultResolutionShareHoldsAMaximalDecoder(t *testing.T) {
	share := DefaultProfileResolutionCacheMiB << 20 / 100 * DefaultProfileResolutionTenantSharePercent
	assert.GreaterOrEqual(t, share, MaxDecoderResolutionChargeBytes)
}
