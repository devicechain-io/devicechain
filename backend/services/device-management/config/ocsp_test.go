// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func defaultOcsp() OcspConfiguration {
	c := OcspConfiguration{}
	c.ApplyDefaults()
	return c
}

func TestOcspDefaultsAreOffAndValid(t *testing.T) {
	c := defaultOcsp()
	require.NoError(t, c.Validate())
	require.False(t, c.Enabled, "the master switch is off by default")
	require.False(t, c.AllowRequireCurrent)
	require.Equal(t, 400*time.Millisecond, c.FetchDeadlineDuration())
	require.Equal(t, 32, c.MaxInFlight)
	require.Equal(t, 6*time.Hour, c.MaxAgeDuration())
	require.Equal(t, 30*time.Second, c.FailureTtlDuration())
	require.Equal(t, 100_000, c.CacheEntries)
	require.Equal(t, 16, c.MaxResponderHostsPerTenant)

	// and through the callout configuration as a whole
	cc := CalloutConfiguration{}
	cc.ApplyDefaults()
	require.NoError(t, cc.Validate())
	require.False(t, cc.Ocsp.Enabled)
}

func TestOcspBoundsAreRefusedNotClamped(t *testing.T) {
	for name, mut := range map[string]func(*OcspConfiguration){
		"fetchDeadline over the max":    func(c *OcspConfiguration) { c.FetchDeadline = "3s" },
		"fetchDeadline negative":        func(c *OcspConfiguration) { c.FetchDeadline = "-1s" },
		"fetchDeadline not a duration":  func(c *OcspConfiguration) { c.FetchDeadline = "fast" },
		"maxInFlight zero":              func(c *OcspConfiguration) { c.MaxInFlight = 0 },
		"maxInFlight over":              func(c *OcspConfiguration) { c.MaxInFlight = MaxOcspMaxInFlight + 1 },
		"maxAge under":                  func(c *OcspConfiguration) { c.MaxAge = "1m" },
		"maxAge over":                   func(c *OcspConfiguration) { c.MaxAge = "169h" },
		"failureTtl under":              func(c *OcspConfiguration) { c.FailureTtl = "100ms" },
		"failureTtl over":               func(c *OcspConfiguration) { c.FailureTtl = "11m" },
		"cacheEntries under":            func(c *OcspConfiguration) { c.CacheEntries = 10 },
		"cacheEntries over":             func(c *OcspConfiguration) { c.CacheEntries = MaxOcspCacheEntries + 1 },
		"hosts negative":                func(c *OcspConfiguration) { c.MaxResponderHostsPerTenant = -1 },
		"hosts over":                    func(c *OcspConfiguration) { c.MaxResponderHostsPerTenant = MaxOcspMaxResponderHostsPerTenant + 1 },
		"allowRequireCurrent while off": func(c *OcspConfiguration) { c.AllowRequireCurrent = true },
	} {
		t.Run(name, func(t *testing.T) {
			c := defaultOcsp()
			mut(&c)
			require.Error(t, c.Validate())
		})
	}
	// The control for each bound: the edge itself is accepted.
	c := defaultOcsp()
	c.FetchDeadline = "0s" // cache-only
	require.NoError(t, c.Validate())
	c.FetchDeadline = "2s"
	require.NoError(t, c.Validate())
	c.Enabled, c.AllowRequireCurrent = true, true
	require.NoError(t, c.Validate())
}
