// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Every ingest counter is built, and the age-limit counter is exported under its documented
// name. The ingester skips a nil counter without a word, so a field left out of buildMetrics
// is a reading dropped uncounted, with every other test still green.
func TestIngestMetricsAreAllBuiltAndTooOldIsExported(t *testing.T) {
	newTestMicroservice(t)
	prev := ingestMetrics
	t.Cleanup(func() { ingestMetrics = prev })
	registry := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(registry)

	buildMetrics()
	v := reflect.ValueOf(ingestMetrics)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "PresenceEmitted" {
			// The one field this service leaves unbuilt, on purpose: LwM2M presence is emitted
			// by the registry, not through the ingester (observe's ingester interface has only
			// Ingest), and is counted by registry.Metrics.PresenceEmitted under the same name.
			// Asserted nil, so wiring it later has to revisit this exemption.
			require.True(t, v.Field(i).IsNil(), "IngestMetrics.PresenceEmitted is built; drop its exemption here")
			continue
		}
		require.False(t, v.Field(i).IsNil(), "IngestMetrics.%s is not built", name)
	}

	ingestMetrics.TooOldDropped.Add(2)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "devicechain_lwm2mingest_telemetry_too_old_dropped_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(2), f.GetMetric()[0].GetCounter().GetValue())
			return
		}
	}
	require.Fail(t, "telemetry_too_old_dropped_total is not registered")
}
