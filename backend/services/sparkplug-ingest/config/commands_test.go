// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

func src(tenant, url string, cmds SourceCommands) SparkplugSource {
	return SparkplugSource{Tenant: tenant, HostId: "host-" + tenant, Broker: SourceBroker{URL: url}, Commands: cmds}
}

func primary() SourceCommands { return SourceCommands{Enabled: true, HostRole: HostRolePrimary} }

func load(t *testing.T, sources ...SparkplugSource) error {
	t.Helper()
	c := &SparkplugConfiguration{Sources: sources}
	c.ApplyDefaults()
	return c.Validate()
}

// Commands are refused on a broker that another source shares, and a sole source loads. The pair is
// the control: only the sharing decides.
func TestCommandsAreRefusedOnASharedBrokerAndAcceptedOnASoleOne(t *testing.T) {
	t.Run("a sole source loads", func(t *testing.T) {
		require.NoError(t, load(t, src("acme", "tcp://broker.plant-a:1883", primary())))
	})
	t.Run("two same-tenant sources on one endpoint: the commanding one is refused", func(t *testing.T) {
		err := load(t,
			src("acme", "tcp://broker.plant-a:1883", primary()),
			SparkplugSource{Tenant: "acme", HostId: "other", Broker: SourceBroker{URL: "tcp://BROKER.plant-a:1883/"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one Sparkplug source per broker")
		assert.Contains(t, err.Error(), "sources[0]")
	})
	t.Run("the same two sources without commands still load", func(t *testing.T) {
		require.NoError(t, load(t,
			src("acme", "tcp://broker.plant-a:1883", SourceCommands{}),
			SparkplugSource{Tenant: "acme", HostId: "other", Broker: SourceBroker{URL: "tcp://broker.plant-a:1883"}}),
			"sharing a broker is fine for ingest; only commands refuse it")
	})
	t.Run("two sources on different brokers each command", func(t *testing.T) {
		require.NoError(t, load(t,
			src("acme", "tcp://broker.plant-a:1883", primary()),
			src("globex", "tcp://broker.plant-b:1883", primary())))
	})
}

func TestEnablingCommandsRequiresAHostRole(t *testing.T) {
	err := load(t, src("acme", "tcp://b:1883", SourceCommands{Enabled: true}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commands.hostRole is required")

	err = load(t, src("acme", "tcp://b:1883", SourceCommands{Enabled: true, HostRole: "boss"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"primary" or "secondary"`)

	require.NoError(t, load(t, src("acme", "tcp://b:1883", SourceCommands{Enabled: true, HostRole: HostRoleSecondary})))
	require.NoError(t, load(t, src("acme", "tcp://b:1883", SourceCommands{}))) // off: no role needed
}

func TestTheWindowsAndRatesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(c *SourceCommands)
		field string
	}{
		{"confirm below floor", func(c *SourceCommands) { c.ConfirmWindowSeconds = MinCommandConfirmWindowSeconds - 1 }, "confirmWindowSeconds"},
		{"confirm above ceiling", func(c *SourceCommands) { c.ConfirmWindowSeconds = MaxCommandConfirmWindowSeconds + 1 }, "confirmWindowSeconds"},
		{"confirm negative", func(c *SourceCommands) { c.ConfirmWindowSeconds = -1 }, "confirmWindowSeconds"},
		{"reboot below floor", func(c *SourceCommands) { c.RebootWindowSeconds = MinCommandRebootWindowSeconds - 1 }, "rebootWindowSeconds"},
		{"reboot above ceiling", func(c *SourceCommands) { c.RebootWindowSeconds = MaxCommandRebootWindowSeconds + 1 }, "rebootWindowSeconds"},
		{"skew negative", func(c *SourceCommands) { c.ClockSkewSeconds = -1 }, "clockSkewSeconds"},
		{"skew above ceiling", func(c *SourceCommands) { c.ClockSkewSeconds = MaxCommandClockSkewSeconds + 1 }, "clockSkewSeconds"},
		{"rate negative", func(c *SourceCommands) { c.PublishPerSecond = -1 }, "publishPerSecond"},
		{"burst negative", func(c *SourceCommands) { c.Burst = -1 }, "burst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := primary()
			tc.edit(&c)
			err := load(t, src("acme", "tcp://b:1883", c))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
		})
	}

	// And the edges of the bounds, which must load.
	for name, edit := range map[string]func(c *SourceCommands){
		"confirm floor":   func(c *SourceCommands) { c.ConfirmWindowSeconds = MinCommandConfirmWindowSeconds },
		"confirm ceiling": func(c *SourceCommands) { c.ConfirmWindowSeconds = MaxCommandConfirmWindowSeconds },
		"reboot floor":    func(c *SourceCommands) { c.RebootWindowSeconds = MinCommandRebootWindowSeconds },
		"reboot ceiling":  func(c *SourceCommands) { c.RebootWindowSeconds = MaxCommandRebootWindowSeconds },
		"skew ceiling":    func(c *SourceCommands) { c.ClockSkewSeconds = MaxCommandClockSkewSeconds },
	} {
		c := primary()
		edit(&c)
		require.NoErrorf(t, load(t, src("acme", "tcp://b:1883", c)), "%s", name)
	}
}

func TestUnsetTuningTakesTheDefaults(t *testing.T) {
	cfg := &SparkplugConfiguration{Sources: []SparkplugSource{src("acme", "tcp://b:1883", primary())}}
	cfg.ApplyDefaults()
	c := cfg.Sources[0].Commands
	assert.Equal(t, 120, c.ConfirmWindowSeconds)
	assert.Equal(t, 600, c.RebootWindowSeconds)
	assert.Equal(t, 30, c.ClockSkewSeconds)
	assert.Equal(t, 50.0, c.PublishPerSecond)
	assert.Equal(t, 200, c.Burst)
	assert.False(t, c.AllowReboot, "reboot must be off by default")
	assert.Empty(t, c.WritableMetrics, "writes must be denied by default")
	assert.False(t, cfg.AnyCommandsEnabled() && !c.Enabled)
	assert.True(t, cfg.AnyCommandsEnabled())
}

// Writes are deny-by-default, an exact name or a "prefix/*" pattern allows, and the protocol's own
// control metrics can be neither listed nor reached by a pattern. Each refusal is paired with the
// entry that is allowed.
func TestTheWritableMetricAllowlist(t *testing.T) {
	c := primary()
	assert.False(t, c.WritableMetric("Setpoint"), "an empty allowlist must allow nothing")

	c.WritableMetrics = []string{"Setpoint", "Outputs/*"}
	for name, want := range map[string]bool{
		"Setpoint":      true,
		"Setpoint2":     false, // exact means exact
		"setpoint":      false,
		"Outputs/Valve": true,
		"Outputs/a/b":   true,
		"Outputs":       false, // the prefix itself is not under it
		"Outputs/":      false,
		"Output/Valve":  false,
		"":              false,
		"bdSeq":         false,
	} {
		assert.Equalf(t, want, c.WritableMetric(name), "WritableMetric(%q)", name)
	}

	// A reserved name is refused even if a list somehow held it.
	c.WritableMetrics = []string{"Node Control/Reboot", "bdSeq"}
	assert.False(t, c.WritableMetric("Node Control/Reboot"))
	assert.False(t, c.WritableMetric("bdSeq"))
}

func TestWritablePatternsAreValidatedAtLoad(t *testing.T) {
	for _, bad := range []string{"", " ", " Setpoint", "*", "/*", "Out*puts", "Outputs/*/x", "**", "Node Control/Reboot", "Node Control/*", "Device Control/Scan Rate", "bdSeq", "Node/*x"} {
		c := primary()
		c.WritableMetrics = []string{bad}
		require.Errorf(t, load(t, src("acme", "tcp://b:1883", c)), "writableMetrics %q should be refused", bad)
	}
	for _, good := range []string{"Setpoint", "Outputs/*", "Plant/Line 1/Speed", "Node", "Nodes/*"} {
		c := primary()
		c.WritableMetrics = []string{good}
		require.NoErrorf(t, load(t, src("acme", "tcp://b:1883", c)), "writableMetrics %q should load", good)
	}
}

// The new keys reach the typed struct through the loader, and an unknown key under commands is
// refused (the repo's fail-closed rule): a misspelled allowlist must not load as "no writes".
func TestCommandsKeysLoadAndUnknownOnesAreRefused(t *testing.T) {
	cfg := NewSparkplugConfiguration()
	require.NoError(t, core.LoadConfiguration([]byte(`{"sources":[{"tenant":"acme","broker":{"url":"tcp://b:1883"},
		"commands":{"enabled":true,"hostRole":"primary","writableMetrics":["Outputs/*"],"allowReboot":true,
		"confirmWindowSeconds":90,"rebootWindowSeconds":300,"clockSkewSeconds":10,"publishPerSecond":5,"burst":9}}]}`), cfg))
	c := cfg.Sources[0].Commands
	assert.True(t, c.Enabled)
	assert.Equal(t, HostRolePrimary, c.HostRole)
	assert.Equal(t, []string{"Outputs/*"}, c.WritableMetrics)
	assert.True(t, c.AllowReboot)
	assert.Equal(t, 90, c.ConfirmWindowSeconds)
	assert.Equal(t, 300, c.RebootWindowSeconds)
	assert.Equal(t, 10, c.ClockSkewSeconds)
	assert.Equal(t, 5.0, c.PublishPerSecond)
	assert.Equal(t, 9, c.Burst)

	bad := NewSparkplugConfiguration()
	assert.Error(t, core.LoadConfiguration([]byte(`{"sources":[{"tenant":"acme","broker":{"url":"tcp://b:1883"},
		"commands":{"enabled":true,"hostRole":"primary","writableMetric":["Outputs/*"]}}]}`), bad),
		"a misspelled commands key loaded silently")
}
