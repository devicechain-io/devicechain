// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const safetyGateMetric = "devicechain_commanddelivery_safety_gate_enabled"

func gatesFixture(t *testing.T) (*SafetyGates, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "command-delivery"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	return NewSafetyGates(ms), reg
}

// gateValue reads the gauge for one gate off the registry by its exported name; absent is
// reported as ok=false, which is a different answer from 0 and the whole point of the gauge.
func gateValue(t *testing.T, reg *prometheus.Registry, gate string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != safetyGateMetric {
			continue
		}
		for _, m := range f.GetMetric() {
			if labelOf(m, "gate") == gate {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func labelOf(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// A gate that came up unconfigured is OFF, and "off" must read 0 — not be an absent series, which
// an alert over `== 0` cannot see. Before the gauge existed it was a startup WARN and nothing else.
func TestAnUnconfiguredLifecycleGateReportsZero(t *testing.T) {
	gates, reg := gatesFixture(t)

	gate := NewTenantLifecycleGate(config.UserManagementConfiguration{}, "", "command-delivery", gates)

	assert.Nil(t, gate)
	v, ok := gateValue(t, reg, GateTenantLifecycle)
	require.True(t, ok, "an OFF gate must be a present series")
	assert.Equal(t, 0.0, v)
}

// The counterweight: a configured gate reads 1, so the gauge is not simply 0 everywhere.
func TestAConfiguredLifecycleGateReportsOne(t *testing.T) {
	gates, reg := gatesFixture(t)

	gate := NewTenantLifecycleGate(config.UserManagementConfiguration{Hostname: "um", Port: 8080}, "s3cret", "command-delivery", gates)

	require.NotNil(t, gate)
	v, ok := gateValue(t, reg, GateTenantLifecycle)
	require.True(t, ok)
	assert.Equal(t, 1.0, v)
}

// A service builds its gates inside lifecycle callbacks that can be entered again; a second
// registration must hand back the same gauge, not panic.
func TestNewSafetyGatesIsIdempotentPerService(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "command-delivery"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	first := NewSafetyGates(ms)
	second := NewSafetyGates(ms)
	first.Set(GatePresence, false)
	second.Set(GateRuleValidation, true)

	for gate, want := range map[string]float64{GatePresence: 0, GateRuleValidation: 1} {
		v, ok := gateValue(t, reg, gate)
		require.Truef(t, ok, "%s must be present through either handle", gate)
		assert.Equal(t, want, v)
	}
}

// A caller built without metrics runs unmeasured rather than panicking.
func TestNilSafetyGatesIsANoOp(t *testing.T) {
	var gates *SafetyGates
	assert.NotPanics(t, func() { gates.Set(GatePresence, false) })
}
