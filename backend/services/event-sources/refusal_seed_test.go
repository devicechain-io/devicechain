// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// The ingest board's "Rejected as malformed" and "Held back by rate limits" panels read
// increase() of these counters. A counter first touched by the refusal is born at its
// post-refusal value and the first burst after a start is invisible, so every refusal
// counter of every configured source must be exported at zero before any traffic.
func TestRefusalCountersAreExportedAtZeroForEveryConfiguredSource(t *testing.T) {
	savedMs, savedConfig := Microservice, Configuration
	t.Cleanup(func() { Microservice, Configuration = savedMs, savedConfig })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	reg := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(reg)
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{Id: "http1"}, {Id: "mqtt1"}},
	}
	initializeMetrics()

	families, err := reg.Gather()
	require.NoError(t, err)
	series := map[string]map[string]int{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "source" {
					if series[f.GetName()] == nil {
						series[f.GetName()] = map[string]int{}
					}
					series[f.GetName()][l.GetValue()]++
					require.Zero(t, m.GetCounter().GetValue(), f.GetName())
				}
			}
		}
	}
	for _, name := range []string{
		"failed_decode", "invalid_event_time", "too_many_readings", "credential_in_mqtt_body",
		"backpressured", "tenant_deleted", "publish_failed",
	} {
		for _, src := range []string{"http1", "mqtt1"} {
			require.Equal(t, 1, series["devicechain_eventsources_total_msg_"+name][src], "%s{source=%s}", name, src)
		}
	}
	for _, name := range []string{"total_msg_rate_limited", "total_msg_reading_limited", "total_readings_rate_limited"} {
		require.Equal(t, 4, series["devicechain_eventsources_"+name]["http1"], "%s: one series per shed class", name)
	}
}
