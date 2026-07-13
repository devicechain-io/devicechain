// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
)

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
	if c.OutboundMessagesPerSecond != DefaultOutboundMessagesPerSecond {
		t.Fatalf("outboundMessagesPerSecond = %v, want %v", c.OutboundMessagesPerSecond, float64(DefaultOutboundMessagesPerSecond))
	}
	if c.OutboundBurst != DefaultOutboundBurst {
		t.Fatalf("outboundBurst = %d, want %d", c.OutboundBurst, DefaultOutboundBurst)
	}
	if c.EgressWaitBudgetMs != DefaultEgressWaitBudgetMs {
		t.Fatalf("egressWaitBudgetMs = %d, want %d", c.EgressWaitBudgetMs, DefaultEgressWaitBudgetMs)
	}
}

// TestApplyDefaultsPreservesSet leaves an explicitly-set value untouched.
func TestApplyDefaultsPreservesSet(t *testing.T) {
	c := &OutboundConnectorsConfiguration{
		SendTimeoutMs: 3000, MaxConcurrentSends: 4, DispatchBacklog: 8,
		OutboundMessagesPerSecond: 25, OutboundBurst: 50, EgressWaitBudgetMs: 2000,
	}
	c.ApplyDefaults()
	if c.SendTimeoutMs != 3000 || c.MaxConcurrentSends != 4 || c.DispatchBacklog != 8 ||
		c.OutboundMessagesPerSecond != 25 || c.OutboundBurst != 50 || c.EgressWaitBudgetMs != 2000 {
		t.Fatalf("ApplyDefaults overwrote a set value: %+v", c)
	}
}

// valid is a fully-populated, valid configuration; each bad case perturbs one field.
func valid() OutboundConnectorsConfiguration {
	return OutboundConnectorsConfiguration{
		SendTimeoutMs: 1, MaxConcurrentSends: 1, DispatchBacklog: 1,
		OutboundMessagesPerSecond: 10, OutboundBurst: 20, EgressWaitBudgetMs: 5000,
	}
}

// TestValidate rejects non-positive / out-of-range tunables fail-closed and accepts a defaulted config.
func TestValidate(t *testing.T) {
	if err := NewOutboundConnectorsConfiguration().Validate(); err != nil {
		t.Fatalf("default config should validate, got %v", err)
	}
	v := valid()
	if err := v.Validate(); err != nil {
		t.Fatalf("valid config should validate, got %v", err)
	}
	bad := map[string]func(*OutboundConnectorsConfiguration){
		"negative sendTimeout":          func(c *OutboundConnectorsConfiguration) { c.SendTimeoutMs = -1 },
		"sendTimeout above the ceiling": func(c *OutboundConnectorsConfiguration) { c.SendTimeoutMs = connectorwire.MaxTimeoutMs + 1 },
		"zero concurrency":              func(c *OutboundConnectorsConfiguration) { c.MaxConcurrentSends = 0 },
		"zero backlog":                  func(c *OutboundConnectorsConfiguration) { c.DispatchBacklog = 0 },
		"zero outbound rate":            func(c *OutboundConnectorsConfiguration) { c.OutboundMessagesPerSecond = 0 },
		"negative outbound rate":        func(c *OutboundConnectorsConfiguration) { c.OutboundMessagesPerSecond = -1 },
		"zero outbound burst":           func(c *OutboundConnectorsConfiguration) { c.OutboundBurst = 0 },
		"zero wait budget":              func(c *OutboundConnectorsConfiguration) { c.EgressWaitBudgetMs = 0 },
		"negative wait budget":          func(c *OutboundConnectorsConfiguration) { c.EgressWaitBudgetMs = -5 },
		"wait budget above the ceiling": func(c *OutboundConnectorsConfiguration) { c.EgressWaitBudgetMs = MaxEgressWaitBudgetMs + 1 },
	}
	for name, perturb := range bad {
		c := valid()
		perturb(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("case %q (%+v): expected a validation error", name, c)
		}
	}
}
