// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The Api reports evictions through a hook the service wires after the metrics exist. Nothing
// else exercises main.go's wiring, and an unwired hook silently counts nothing, so this pins
// that wireDroppedEvictions connects the Api to dropped_measurement_evicted_total.
func TestTheApiReportsEvictionsToTheCounter(t *testing.T) {
	savedMs, savedState, savedSweep, savedDropped := Microservice, StateMetrics, InactivitySweepMetrics, DroppedEvicted
	t.Cleanup(func() {
		Microservice, StateMetrics, InactivitySweepMetrics, DroppedEvicted = savedMs, savedState, savedSweep, savedDropped
	})
	Microservice = &core.Microservice{InstanceId: "i", FunctionalArea: "device-state"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	buildMetrics()
	require.NotNil(t, DroppedEvicted)

	api := model.NewApi(nil)
	require.Nil(t, api.OnDroppedEvicted, "negative control: an Api that was not wired reports nowhere")

	wireDroppedEvictions(api)
	require.NotNil(t, api.OnDroppedEvicted)
	api.OnDroppedEvicted(3)
	api.OnDroppedEvicted(2)
	require.EqualValues(t, 5, testutil.ToFloat64(DroppedEvicted))
}
