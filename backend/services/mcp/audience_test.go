// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	regMCP   = "https://iot.example.com/api/mcp"
	regOther = "https://other.example.com/api/mcp"
	gauge    = "devicechain_mcp_token_audience_registered"
)

func cfgWith(mode string, mcp ...string) config.TokenAudienceConfiguration {
	return config.TokenAudienceConfiguration{
		Enforcement: mode, AreaBaseUrl: "https://iot.example.com/api", McpResources: mcp,
	}
}

func gaugeValue(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	n, err := testutil.GatherAndCount(reg, gauge)
	require.NoError(t, err)
	require.Equal(t, 1, n, "exactly one series")
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == gauge {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("gauge missing")
	return -1
}

func TestEnforceRefusesToStartWhenResourceUrlIsNotRegistered(t *testing.T) {
	for name, mcp := range map[string][]string{
		"empty list":      nil,
		"another value":   {regOther},
		"trailing slash":  {regMCP + "/"},
		"different case":  {"https://IOT.example.com/api/mcp"},
		"prefix only":     {"https://iot.example.com/api"},
		"registered also": {regOther, "https://iot.example.com/api/mcp2"},
	} {
		t.Run(name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			err := checkAudienceRegistration(regMCP, cfgWith("enforce", mcp...), reg, "mcp")
			require.Error(t, err)
			require.Contains(t, err.Error(), "infrastructure.tokenAudience.mcpResources", "names the key")
			require.Contains(t, err.Error(), regMCP)
			require.Equal(t, 0.0, gaugeValue(t, reg))
		})
	}
}

func TestObserveWarnsButStartsWhenResourceUrlIsNotRegistered(t *testing.T) {
	for _, mode := range []string{"observe", ""} { // "" is the v1.2 default
		t.Run("mode="+mode, func(t *testing.T) {
			logs := logSink.Capture(t)
			reg := prometheus.NewRegistry()
			require.NoError(t, checkAudienceRegistration(regMCP, cfgWith(mode, regOther), reg, "mcp"))
			require.Equal(t, 0.0, gaugeValue(t, reg))
			require.Contains(t, logs.String(), `"level":"error"`)
			require.Contains(t, logs.String(), "not a registered MCP resource")
		})
	}
}

// The counterweight: a registered resourceUrl starts in both modes, gauge 1, no error line.
func TestRegisteredResourceUrlStartsInBothModes(t *testing.T) {
	for _, mode := range []string{"enforce", "observe", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			logs := logSink.Capture(t)
			reg := prometheus.NewRegistry()
			require.NoError(t, checkAudienceRegistration(regMCP, cfgWith(mode, regOther, regMCP), reg, "mcp"))
			require.Equal(t, 1.0, gaugeValue(t, reg))
			require.NotContains(t, logs.String(), "not a registered MCP resource")
		})
	}
}

// A retried initialize re-runs the check on the same registry: it must neither panic nor fail
// on the duplicate registration, and the gauge follows the latest verdict.
func TestCheckIsRepeatableOnOneRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, checkAudienceRegistration(regMCP, cfgWith("observe", regOther), reg, "mcp"))
	require.Equal(t, 0.0, gaugeValue(t, reg))
	require.NoError(t, checkAudienceRegistration(regMCP, cfgWith("observe", regMCP), reg, "mcp"))
	require.Equal(t, 1.0, gaugeValue(t, reg))
}

// The REAL initializer: in enforce mode with an unregistered resourceUrl it returns the error
// before it registers a route or starts the auth gate, so the process exits instead of
// reporting healthy while the planes refuse everything it forwards.
func TestInitializerRefusesToStartUnderEnforce(t *testing.T) {
	prevMs, prevCfg := Microservice, Configuration
	t.Cleanup(func() { Microservice, Configuration = prevMs, prevCfg })

	Microservice = &core.Microservice{
		InstanceId: "test", FunctionalArea: "mcp", Readiness: core.NewReadinessGate(),
		MicroserviceConfigurationRaw: []byte(`{"resourceUrl":"` + regMCP + `","issuerUrl":"https://iot.example.com/api/user-management"}`),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.TokenAudience = cfgWith("enforce", regOther)

	err := afterMicroserviceInitialized(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "infrastructure.tokenAudience.mcpResources")
}
