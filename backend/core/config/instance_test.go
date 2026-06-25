// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// The shipped default instance configuration applies defaults cleanly and passes
// validation.
func TestDefaultInstanceConfigurationValid(t *testing.T) {
	cfg := NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	if cfg.Infrastructure.Nats.StreamReplicas == 0 {
		t.Fatal("ApplyDefaults should default NATS stream replicas to 1")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default instance configuration should be valid, got %v", err)
	}
}

// Validate fails closed when the NATS backbone or the user-management endpoint
// (which every service validates tokens against) is missing.
func TestInstanceConfigurationValidateFailsClosed(t *testing.T) {
	cases := map[string]func(*InstanceConfiguration){
		"missing nats host": func(c *InstanceConfiguration) { c.Infrastructure.Nats.Hostname = "" },
		"missing nats port": func(c *InstanceConfiguration) { c.Infrastructure.Nats.Port = 0 },
		"missing um host":   func(c *InstanceConfiguration) { c.Infrastructure.UserManagement.Hostname = "" },
		"missing um port":   func(c *InstanceConfiguration) { c.Infrastructure.UserManagement.Port = 0 },
	}
	for name, mutate := range cases {
		cfg := NewDefaultInstanceConfiguration()
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}
