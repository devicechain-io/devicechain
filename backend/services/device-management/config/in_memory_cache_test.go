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
	assert.Equal(t, 24, DefaultPerDeviceCacheMiB)
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
