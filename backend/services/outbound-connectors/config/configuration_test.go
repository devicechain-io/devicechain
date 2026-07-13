// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// TestApplyDefaults fills every unset tunable with its platform default.
func TestApplyDefaults(t *testing.T) {
	c := &OutboundConnectorsConfiguration{}
	c.ApplyDefaults()
	if c.SendTimeoutMs != DefaultSendTimeoutMs {
		t.Fatalf("sendTimeoutMs = %d, want %d", c.SendTimeoutMs, DefaultSendTimeoutMs)
	}
	if c.MaxConcurrentSends != DefaultMaxConcurrentSends {
		t.Fatalf("maxConcurrentSends = %d, want %d", c.MaxConcurrentSends, DefaultMaxConcurrentSends)
	}
	if c.DispatchBacklog != DefaultDispatchBacklog {
		t.Fatalf("dispatchBacklog = %d, want %d", c.DispatchBacklog, DefaultDispatchBacklog)
	}
}

// TestApplyDefaultsPreservesSet leaves an explicitly-set value untouched.
func TestApplyDefaultsPreservesSet(t *testing.T) {
	c := &OutboundConnectorsConfiguration{SendTimeoutMs: 3000, MaxConcurrentSends: 4, DispatchBacklog: 8}
	c.ApplyDefaults()
	if c.SendTimeoutMs != 3000 || c.MaxConcurrentSends != 4 || c.DispatchBacklog != 8 {
		t.Fatalf("ApplyDefaults overwrote a set value: %+v", c)
	}
}

// TestValidate rejects non-positive tunables fail-closed and accepts a defaulted config.
func TestValidate(t *testing.T) {
	if err := NewOutboundConnectorsConfiguration().Validate(); err != nil {
		t.Fatalf("default config should validate, got %v", err)
	}
	bad := []OutboundConnectorsConfiguration{
		{SendTimeoutMs: -1, MaxConcurrentSends: 1, DispatchBacklog: 1},
		{SendTimeoutMs: 1, MaxConcurrentSends: 0, DispatchBacklog: 1},
		{SendTimeoutMs: 1, MaxConcurrentSends: 1, DispatchBacklog: 0},
		{SendTimeoutMs: 1, MaxConcurrentSends: -2, DispatchBacklog: 1},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d (%+v): expected a validation error", i, c)
		}
	}
}
