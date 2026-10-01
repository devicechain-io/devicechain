// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The projection settings in the service's configuration document reach the processor it
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
	Microservice.MicroserviceConfigurationRaw = []byte(`{"projection":{"writers":3,"maxBatch":8,"lingerMillis":2}}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	sp := newStateProcessor(nil)
	require.Equal(t, 3, sp.Writers())
	require.Equal(t, 8, sp.MaxBatch())
	require.Equal(t, 2*time.Millisecond, sp.Linger())

	// A document that sets none runs the defaults, stated here as literals.
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	sp = newStateProcessor(nil)
	require.Equal(t, 10, sp.Writers())
	require.Equal(t, 32, sp.MaxBatch())
	require.Equal(t, time.Duration(0), sp.Linger())
}
