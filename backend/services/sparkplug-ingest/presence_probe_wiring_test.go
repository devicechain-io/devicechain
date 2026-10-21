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
		name       string
		seconds    int
		window     int
		want       time.Duration
		wantWindow time.Duration
	}{
		{"unset", 0, 0, config.DefaultPresenceProbeSeconds * time.Second, config.DefaultPresenceProbeWindowSeconds * time.Second},
		{"written", 900, 25, 900 * time.Second, 25 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.SparkplugConfiguration{
				Sources:  []config.SparkplugSource{src("tcp://a:1883"), src("ssl://b:8883")},
				Presence: config.Presence{ProbeSeconds: tc.seconds, ProbeWindowSeconds: tc.window},
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
				assert.Equal(t, tc.wantWindow, c.PresenceProbeWindow(), "sources[%d] waits a different answer window than configured", i)
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
	require.NotNil(t, m.PresenceProbePass, "the periodic probe files its passes on the shared series")
	// Counted BEFORE anything reads a child: WithLabelValues creates the child it names, so
	// a count taken after the reads below would find both children whether or not the
	// service created them.
	assert.Equal(t, 2, testutil.CollectAndCount(m.PresenceProbes), "both trigger children exist at 0 before any probe ran")
	assert.Equal(t, 2, testutil.CollectAndCount(m.PresenceProbeDisconnects), "both trigger children exist at 0 before any probe ran")
	for _, tr := range []string{host.ProbeTriggerConnect, host.ProbeTriggerPeriodic} {
		assert.Equal(t, float64(0), testutil.ToFloat64(m.PresenceProbes.WithLabelValues(tr)))
		assert.Equal(t, float64(0), testutil.ToFloat64(m.PresenceProbeDisconnects.WithLabelValues(tr)))
	}
}

// The two probe counters are exported under their own names: swapping the fields in
// buildMetrics would publish probe verdicts as disconnects, and the precreate test above,
// which reads only zeros, could not tell.
func TestProbeCountersAreExportedUnderTheirOwnNames(t *testing.T) {
	registry := newTestMicroservice(t)
	m := buildMetrics()
	m.PresenceProbes.WithLabelValues(host.ProbeTriggerPeriodic).Add(2)
	m.PresenceProbeDisconnects.WithLabelValues(host.ProbeTriggerPeriodic).Add(5)
	got := map[string]float64{}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		for _, mm := range f.GetMetric() {
			for _, l := range mm.GetLabel() {
				if l.GetName() == "trigger" && l.GetValue() == host.ProbeTriggerPeriodic {
					got[f.GetName()] = mm.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Equal(t, float64(2), got["devicechain_sparkplugingest_presence_probes_total"])
	assert.Equal(t, float64(5), got["devicechain_sparkplugingest_presence_probe_disconnects_total"])
}
