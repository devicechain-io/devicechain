// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	tenant, source string
	state          host.BreakerState
}

func (f fakeSource) BreakerSample() (string, string, host.BreakerState) {
	return f.tenant, f.source, f.state
}

// The gauge's value and its tenant and source labels are what they were; the vpartition label
// is the one core/partition computes for that tenant, and an unchanged series count.
func TestBreakerGaugeCarriesTheVPartitionOfItsTenant(t *testing.T) {
	const instance = "dc-sparkplug"
	var sources []breakerSampler
	for i := 0; i < 500; i++ {
		sources = append(sources, fakeSource{
			tenant: fmt.Sprintf("tenant-%d", i), source: fmt.Sprintf("src-%d", i%7),
			state: host.BreakerState(i % 3),
		})
	}
	samples := breakerGaugeSamples(instance, sources)
	require.Len(t, samples, len(sources), "the label adds no series")

	seen := map[string]bool{}
	for i, s := range samples {
		f := sources[i].(fakeSource)
		require.Len(t, s.Labels, len(breakerGaugeLabels))
		require.Equal(t, f.tenant, s.Labels[0])
		require.Equal(t, f.source, s.Labels[2])
		require.Equal(t, float64(f.state), s.Value)
		// lockstep with core/partition
		require.Equal(t, partition.Label(partition.NewTable(partition.DefaultP).VPartition(instance, f.tenant)), s.Labels[1])
		seen[s.Labels[1]] = true
	}
	require.LessOrEqual(t, len(seen), partition.DefaultP)
}

// Exposed through the real collector, the metric has exactly the declared label names.
func TestBreakerGaugeExposesTenantVPartitionAndSource(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "sparkplug-ingest"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	ms.NewScrapedGaugeVec("command_breaker_state", "test", breakerGaugeLabels, func() []core.GaugeSample {
		return breakerGaugeSamples("dc", []breakerSampler{fakeSource{tenant: "acme", source: "plant", state: 1}})
	})
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	m := families[0].GetMetric()
	require.Len(t, m, 1)
	got := map[string]string{}
	for _, l := range m[0].GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	require.Equal(t, map[string]string{
		"tenant": "acme", "source": "plant", "vpartition": partition.Label(partition.HashedVPartition("dc", "acme", partition.DefaultP)),
	}, got)
	require.Equal(t, float64(1), m[0].GetGauge().GetValue())
}
