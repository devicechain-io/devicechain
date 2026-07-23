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
	if c.Local.SpoolMaxBytes != DefaultSpoolMaxBytes {
		t.Errorf("spoolMaxBytes default = %d, want %d", c.Local.SpoolMaxBytes, DefaultSpoolMaxBytes)
	}
	if c.Local.MetricsPort == nil || *c.Local.MetricsPort != DefaultMetricsPort {
		t.Errorf("metricsPort default = %v, want %d", c.Local.MetricsPort, DefaultMetricsPort)
	}
}

// An explicit metricsPort of 0 (disabled) must be PRESERVED, not defaulted back to 9090 —
// that is the whole reason the field is a pointer (nil = omitted → default; 0 = off).
func TestMetricsPortZeroIsPreserved(t *testing.T) {
	c := validConfig()
	zero := 0
	c.Local.MetricsPort = &zero
	c.ApplyDefaults()
	if c.Local.MetricsPort == nil || *c.Local.MetricsPort != 0 {
		t.Errorf("explicit metricsPort 0 was overwritten to %v; a pointer must let 0 mean disabled", c.Local.MetricsPort)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("metricsPort 0 (disabled) should validate: %v", err)
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

// Both halves of the optional local-auth credential set together is valid (the enabled
// case), and both omitted is valid (the open trusted-LAN default) — the both-or-neither
// rule rejects only a lone half (covered in TestValidateRejects).
func TestLocalAuthBothOrNeitherAccepts(t *testing.T) {
	both := validConfig()
	both.Local.Username = "edge"
	both.Local.PasswordEnv = "DC_EDGE_LOCAL_PASSWORD"
	if err := loaded(both); err != nil {
		t.Errorf("username+passwordEnv together should validate: %v", err)
	}
	// neither is the default validConfig() — already exercised by TestValidateAcceptsValid,
	// asserted here explicitly for the pairing.
	if err := loaded(validConfig()); err != nil {
		t.Errorf("neither username nor passwordEnv (open listener) should validate: %v", err)
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
		{"spoolMaxBytes below floor", func(c *Configuration) { c.Local.SpoolMaxBytes = 1024 }, "below the"},
		{"metricsPort out of range", func(c *Configuration) { p := 70000; c.Local.MetricsPort = &p }, "out of range"},
		{"metricsPort negative", func(c *Configuration) { p := -1; c.Local.MetricsPort = &p }, "out of range"},
		{"local username without passwordEnv", func(c *Configuration) { c.Local.Username = "edge" }, "must be set together"},
		{"local passwordEnv without username", func(c *Configuration) { c.Local.PasswordEnv = "DC_EDGE_LOCAL_PASSWORD" }, "must be set together"},
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
