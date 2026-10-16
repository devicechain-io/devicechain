// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// Every client the service builds runs its periodic liveness probe at the CONFIGURED
// interval. The host defaults the interval itself, so a service that stopped passing the
// setting would run every source at the platform default while every host test stayed
// green; this is the gate on the wiring, by value, and the reason the client exposes the
// interval it was given.
func TestResolveSourcesPassesTheConfiguredProbeInterval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{"unset", 0, config.DefaultPresenceProbeSeconds * time.Second},
		{"written", 900, 900 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.SparkplugConfiguration{
				Sources:  []config.SparkplugSource{src("tcp://a:1883"), src("ssl://b:8883")},
				Presence: config.Presence{ProbeSeconds: tc.seconds},
			}
			cfg.ApplyDefaults()
			require.NoError(t, cfg.Validate())
			// Decode-only clients (no ingester) are enough: the interval is set on every
			// client regardless of its ingest path.
			clients, err := resolveSources(cfg, "inst", nil, nil, nil, host.Metrics{}, nil)
			require.NoError(t, err)
			require.Len(t, clients, 2)
			for i, c := range clients {
				assert.Equal(t, tc.want, c.PresenceProbeInterval(), "sources[%d] runs at a different interval than configured", i)
			}
		})
	}
}

// The probe counters the service registers carry both trigger children from the start, so
// a dashboard reads a flat periodic series on a Host whose probe has not yet reached a
// verdict rather than no series at all — the whole reason the periodic series exists.
func TestProbeMetricsPrecreateBothTriggers(t *testing.T) {
	newTestMicroservice(t)
	m := buildMetrics()
	require.NotNil(t, m.PresenceProbes)
	require.NotNil(t, m.PresenceProbeDisconnects)
	for _, tr := range []string{host.ProbeTriggerConnect, host.ProbeTriggerPeriodic} {
		assert.Equal(t, float64(0), testutil.ToFloat64(m.PresenceProbes.WithLabelValues(tr)))
		assert.Equal(t, float64(0), testutil.ToFloat64(m.PresenceProbeDisconnects.WithLabelValues(tr)))
	}
	assert.Equal(t, 2, testutil.CollectAndCount(m.PresenceProbes), "both trigger children exist at 0")
	assert.Equal(t, 2, testutil.CollectAndCount(m.PresenceProbeDisconnects), "both trigger children exist at 0")
}
