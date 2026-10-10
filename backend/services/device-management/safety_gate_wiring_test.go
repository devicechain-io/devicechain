// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateReading builds the safety-gate gauge the way the service does (once, on a registry) and
// returns the handle with a reader for one gate's value. present=false is a different answer
// from 0: an absent series is a gate nobody reported, which no alert can see.
func gateReading(t *testing.T, area string) (*governance.SafetyGates, func(gate string) (float64, bool)) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	gates := governance.NewSafetyGates(ms)
	return gates, func(gate string) (float64, bool) {
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, f := range families {
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "gate" && l.GetValue() == gate {
						return m.GetGauge().GetValue(), true
					}
				}
			}
		}
		return 0, false
	}
}

func fullInfra() mscfg.InfrastructureConfiguration {
	var infra mscfg.InfrastructureConfiguration
	infra.ServiceAuth.Secret = "s3cret"
	infra.UserManagement.Hostname, infra.UserManagement.Port = "user-management", 8080
	infra.DeviceState.Hostname, infra.DeviceState.Port = "device-state", 8080
	infra.EventProcessing.Hostname, infra.EventProcessing.Port = "event-processing", 8080
	return infra
}

// The rule-validation gate is OFF (nil validator, gauge 0) when the secret or the event-processing
// endpoint is absent and ON (validator, gauge 1) otherwise; the callout tenant gate records
// tenant_lifecycle the same way. Each is recorded on its own gate and no other.
func TestDetectionRuleValidatorRecordsItsDecision(t *testing.T) {
	for name, mutate := range map[string]func(*mscfg.InfrastructureConfiguration){
		"no secret":   func(i *mscfg.InfrastructureConfiguration) { i.ServiceAuth.Secret = "" },
		"no hostname": func(i *mscfg.InfrastructureConfiguration) { i.EventProcessing.Hostname = "" },
		"no port":     func(i *mscfg.InfrastructureConfiguration) { i.EventProcessing.Port = 0 },
	} {
		gates, read := gateReading(t, "device-management")
		infra := fullInfra()
		mutate(&infra)
		assert.Nil(t, detectionRuleValidator(infra, gates), name)
		v, ok := read(governance.GateRuleValidation)
		assert.True(t, ok, "%s: an OFF gate must be a present series", name)
		assert.Equal(t, 0.0, v, name)
	}

	gates, read := gateReading(t, "device-management")
	assert.NotNil(t, detectionRuleValidator(fullInfra(), gates))
	v, ok := read(governance.GateRuleValidation)
	assert.True(t, ok)
	assert.Equal(t, 1.0, v)
	_, other := read(governance.GateTenantLifecycle)
	assert.False(t, other, "the validator decision must not be recorded on the lifecycle gate")
}

func TestCalloutTenantGateRecordsItsDecision(t *testing.T) {
	gates, read := gateReading(t, "device-management")
	infra := fullInfra()
	infra.ServiceAuth.Secret = ""
	assert.Nil(t, calloutTenantGate(infra, gates))
	v, ok := read(governance.GateTenantLifecycle)
	assert.True(t, ok)
	assert.Equal(t, 0.0, v)

	gates, read = gateReading(t, "device-management")
	assert.NotNil(t, calloutTenantGate(fullInfra(), gates))
	v, ok = read(governance.GateTenantLifecycle)
	assert.True(t, ok)
	assert.Equal(t, 1.0, v)
}
