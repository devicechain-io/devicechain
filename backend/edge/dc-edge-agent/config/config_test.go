// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// validConfig is a minimal document that passes Validate after ApplyDefaults. Each
// negative test starts from a copy and breaks exactly one field, so a failure names
// the constraint under test.
func validConfig() Configuration {
	return Configuration{
		InstanceId: "prod1",
		AgentId:    "site42",
		Local:      LocalConfiguration{StoreDir: "/var/lib/dc-edge-agent"},
		Uplink:     UplinkConfiguration{BrokerURL: "ssl://cloud.example.com:8883"},
	}
}

// loaded applies defaults then validates, mirroring core.LoadConfiguration's order.
func loaded(c Configuration) error {
	c.ApplyDefaults()
	return c.Validate()
}

func TestApplyDefaultsFillsZeroValues(t *testing.T) {
	c := validConfig()
	c.ApplyDefaults()
	if c.Local.ListenHost != "0.0.0.0" {
		t.Errorf("listenHost default = %q, want 0.0.0.0", c.Local.ListenHost)
	}
	if c.Local.ListenPort != 1883 {
		t.Errorf("listenPort default = %d, want 1883", c.Local.ListenPort)
	}
	if c.Uplink.ConnectTimeoutSeconds != 30 {
		t.Errorf("connectTimeoutSeconds default = %d, want 30", c.Uplink.ConnectTimeoutSeconds)
	}
	if c.Uplink.BackoffMinSeconds != 1 || c.Uplink.BackoffMaxSeconds != 60 {
		t.Errorf("backoff defaults = %d/%d, want 1/60", c.Uplink.BackoffMinSeconds, c.Uplink.BackoffMaxSeconds)
	}
}

func TestApplyDefaultsDoesNotOverrideSetValues(t *testing.T) {
	c := validConfig()
	c.Local.ListenHost = "127.0.0.1"
	c.Local.ListenPort = 11883
	c.ApplyDefaults()
	if c.Local.ListenHost != "127.0.0.1" || c.Local.ListenPort != 11883 {
		t.Errorf("ApplyDefaults overrode set values: %q:%d", c.Local.ListenHost, c.Local.ListenPort)
	}
}

func TestValidateAcceptsValid(t *testing.T) {
	for _, scheme := range []string{"tcp", "ssl", "tls"} {
		c := validConfig()
		c.Uplink.BrokerURL = scheme + "://host:1883"
		if err := loaded(c); err != nil {
			t.Errorf("scheme %q: unexpected error: %v", scheme, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Configuration)
		want   string
	}{
		{"missing instanceId", func(c *Configuration) { c.InstanceId = "" }, "instanceId is required"},
		{"bad instanceId grammar", func(c *Configuration) { c.InstanceId = "prod/1" }, "not token-safe"},
		{"instanceId leading dash", func(c *Configuration) { c.InstanceId = "-prod" }, "not token-safe"},
		{"missing agentId", func(c *Configuration) { c.AgentId = "" }, "agentId is required"},
		{"bad agentId grammar", func(c *Configuration) { c.AgentId = "site/42" }, "not token-safe"},
		{"missing storeDir", func(c *Configuration) { c.Local.StoreDir = "" }, "storeDir is required"},
		{"port out of range", func(c *Configuration) { c.Local.ListenPort = 70000 }, "out of range"},
		{"missing brokerUrl", func(c *Configuration) { c.Uplink.BrokerURL = "" }, "brokerUrl is required"},
		{"bad scheme", func(c *Configuration) { c.Uplink.BrokerURL = "http://host:80" }, "unsupported"},
		{"brokerUrl with credentials", func(c *Configuration) { c.Uplink.BrokerURL = "tcp://u:p@host:1883" }, "must not embed credentials"},
		{"brokerUrl no host", func(c *Configuration) { c.Uplink.BrokerURL = "tcp://:1883" }, "has no host"},
		{"brokerUrl no port", func(c *Configuration) { c.Uplink.BrokerURL = "tcp://host" }, "has no port"},
		{"backoff inverted", func(c *Configuration) {
			c.Uplink.BackoffMinSeconds = 30
			c.Uplink.BackoffMaxSeconds = 5
		}, "must be >= backoffMinSeconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := loaded(c)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// The port-out-of-range check must fire on the DEFAULTED value too: a document that
// omits listenPort gets 1883 (valid), so the guard must not reject the zero value
// as "out of range" before ApplyDefaults runs. LoadConfiguration guarantees that
// order; this pins it.
func TestPortDefaultIsValid(t *testing.T) {
	c := validConfig() // ListenPort left 0
	if err := loaded(c); err != nil {
		t.Fatalf("defaulted port should be valid: %v", err)
	}
}
