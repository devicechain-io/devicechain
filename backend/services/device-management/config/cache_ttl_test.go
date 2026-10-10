// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The defaults are five minutes, every one of them: a cache that expires before a device
// reports again answers nothing, and the fleets that report every ten seconds to every few
// minutes are the common case. The numbers are stated in the chart and the docs.
func TestTheCacheTimesToLiveDefaultToFiveMinutes(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), cfg))
	for name, got := range map[string]int{
		"deviceCacheTtlSeconds":       cfg.DeviceCacheTtlSeconds,
		"relationshipCacheTtlSeconds": cfg.RelationshipCacheTtlSeconds,
		"metricDefCacheTtlSeconds":    cfg.MetricDefCacheTtlSeconds,
		"membershipCacheTtlSeconds":   cfg.MembershipCacheTtlSeconds,
		"inMemoryCache.ttlSeconds":    cfg.InMemoryCache.TtlSeconds,
	} {
		assert.Equal(t, 300, got, name)
	}
	assert.Equal(t, 3600, MaxCacheTtlSeconds)
	assert.Equal(t, 32, cfg.InMemoryCache.CredentialCacheMiB)
}

// A value written in the document is kept, the top of the range is accepted, and anything
// outside 1..3600 fails the load, naming the key. Zero is the "unset" that takes the default,
// so it never reaches Validate as a request for no caching.
func TestTheCacheTimesToLiveAreBounded(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(
		`{"deviceCacheTtlSeconds":1,"relationshipCacheTtlSeconds":3600,"inMemoryCache":{"ttlSeconds":90,"credentialCacheMiB":256}}`), cfg))
	assert.Equal(t, 1, cfg.DeviceCacheTtlSeconds)
	assert.Equal(t, 3600, cfg.RelationshipCacheTtlSeconds)
	assert.Equal(t, 90, cfg.InMemoryCache.TtlSeconds)

	for _, tc := range []struct{ doc, key string }{
		{`{"deviceCacheTtlSeconds":-1}`, "deviceCacheTtlSeconds"},
		{`{"deviceCacheTtlSeconds":3601}`, "deviceCacheTtlSeconds"},
		{`{"relationshipCacheTtlSeconds":-5}`, "relationshipCacheTtlSeconds"},
		{`{"relationshipCacheTtlSeconds":86400}`, "relationshipCacheTtlSeconds"},
		{`{"metricDefCacheTtlSeconds":3601}`, "metricDefCacheTtlSeconds"},
		{`{"membershipCacheTtlSeconds":-1}`, "membershipCacheTtlSeconds"},
		{`{"membershipCacheTtlSeconds":7200}`, "membershipCacheTtlSeconds"},
		{`{"inMemoryCache":{"ttlSeconds":-1}}`, "inMemoryCache.ttlSeconds"},
		{`{"inMemoryCache":{"ttlSeconds":3601}}`, "inMemoryCache.ttlSeconds"},
		{`{"inMemoryCache":{"credentialCacheMiB":-1}}`, "inMemoryCache.credentialCacheMiB"},
		{`{"inMemoryCache":{"credentialCacheMiB":257}}`, "inMemoryCache.credentialCacheMiB"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}
