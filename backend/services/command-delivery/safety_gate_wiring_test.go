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

// The presence gate is OFF (nil reader, gauge 0) when the secret or the device-state endpoint is
// absent, and ON (reader, gauge 1) when both are set. Each missing coordinate is tried alone, and
// the decision must be recorded on the presence gate and on no other.
func TestPresenceReaderRecordsItsDecision(t *testing.T) {
	for name, mutate := range map[string]func(*mscfg.InfrastructureConfiguration){
		"no secret":   func(i *mscfg.InfrastructureConfiguration) { i.ServiceAuth.Secret = "" },
		"no hostname": func(i *mscfg.InfrastructureConfiguration) { i.DeviceState.Hostname = "" },
		"no port":     func(i *mscfg.InfrastructureConfiguration) { i.DeviceState.Port = 0 },
	} {
		gates, read := gateReading(t, "command-delivery")
		infra := fullInfra()
		mutate(&infra)
		assert.Nil(t, presenceReader(infra, gates), name)
		v, ok := read(governance.GatePresence)
		assert.True(t, ok, "%s: an OFF gate must be a present series", name)
		assert.Equal(t, 0.0, v, name)
	}

	gates, read := gateReading(t, "command-delivery")
	assert.NotNil(t, presenceReader(fullInfra(), gates))
	v, ok := read(governance.GatePresence)
	assert.True(t, ok)
	assert.Equal(t, 1.0, v)
	_, other := read(governance.GateRuleValidation)
	assert.False(t, other, "the presence decision must not be recorded on another gate")
}
