// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// projection.writers in the service's configuration document reaches the processor it
// builds. Nothing else runs this wiring: a processor test builds the processor itself, so a
// service that stopped passing its configuration would run the default while every other
// test stayed green.
func TestConfiguredProjectionWritersReachTheProcessor(t *testing.T) {
	savedMs, savedCfg := Microservice, Configuration
	savedState, savedSweep := StateMetrics, InactivitySweepMetrics
	t.Cleanup(func() {
		Microservice, Configuration = savedMs, savedCfg
		StateMetrics, InactivitySweepMetrics = savedState, savedSweep
	})

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{"projection":{"writers":3}}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	require.Equal(t, 3, newStateProcessor(nil).Writers())
}
