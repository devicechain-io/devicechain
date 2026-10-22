// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const probePassName = "devicechain_sparkplugingest_presence_probe_passes_total"

func probePassCount(t *testing.T, reg *prometheus.Registry, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != probePassName {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

type failingReconciler struct{ err error }

func (f failingReconciler) AssertedActive(context.Context, string, string) ([]AssertedDevice, uint64, error) {
	return nil, 0, f.err
}

func probePassClient(rec reconcileSource) (*Client, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "sparkplug-ingest"}
	ms.UseMetricsRegistry(reg)
	probes, disconnects := probeMetrics2()
	c, _ := loopClient(&fakeIngester{}, rec, Metrics{
		PresenceProbes: probes, PresenceProbeDisconnects: disconnects,
		PresenceProbePass: ms.NewPeriodicTaskMetrics("presence_probe"),
	})
	return c, reg
}

func probeMetrics2() (*prometheus.CounterVec, *prometheus.CounterVec) {
	_, p, d := probeMetrics()
	return p, d
}

// TestThePeriodicProbeFilesItsPasses drives presenceProbeLoop (the caller), so a loop that
// stopped recording, or stopped running its slots, fails here.
func TestThePeriodicProbeFilesItsPasses(t *testing.T) {
	t.Run("a slot that read the projection is a completed pass, and a stop is not a failure", func(t *testing.T) {
		rec := &countingReconciler{max: loopSession}
		c, reg := probePassClient(rec)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { c.presenceProbeLoop(ctx); close(done) }()

		require.True(t, eventually(2*time.Second, func() bool { return probePassCount(t, reg, core.PassComplete) >= 2 }),
			"the loop ran slots but filed no completed pass")
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the probe loop did not stop when its session context was cancelled")
		}
		assert.Zero(t, probePassCount(t, reg, core.PassFailed))
	})

	t.Run("a slot that could not read device-state is a failed pass", func(t *testing.T) {
		c, reg := probePassClient(failingReconciler{err: errors.New("device-state unreachable")})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go c.presenceProbeLoop(ctx)

		require.True(t, eventually(2*time.Second, func() bool { return probePassCount(t, reg, core.PassFailed) >= 1 }),
			"an unreadable projection must show on the failure series")
		assert.Zero(t, probePassCount(t, reg, core.PassComplete),
			"a slot that decided nothing must not refresh the last-success stamp")
	})
}

// Every source's client is built from one Metrics, so they share one presence_probe series, but
// each probe loop must keep its own failure-log streak: shared, one source's healthy slots would
// log a false "recovered" and end another source's streak, so a failing source logged every slot.
func TestEachClientKeepsItsOwnProbeFailureStreak(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "sparkplug-ingest"}
	ms.UseMetricsRegistry(reg)
	m := Metrics{PresenceProbePass: ms.NewPeriodicTaskMetrics("presence_probe")}
	a := NewClient(config.SparkplugSource{Tenant: "a"}, Broker{}, nil, nil, time.Now, m)
	b := NewClient(config.SparkplugSource{Tenant: "b"}, Broker{}, nil, nil, time.Now, m)
	require.NotNil(t, a.metrics.PresenceProbePass)
	require.NotNil(t, b.metrics.PresenceProbePass)
	assert.NotSame(t, a.metrics.PresenceProbePass, b.metrics.PresenceProbePass,
		"two sources' probe loops share one failure-log streak")
	assert.NotSame(t, m.PresenceProbePass, a.metrics.PresenceProbePass)
	// Still the one series.
	a.metrics.PresenceProbePass.RecordPass(context.Background(), errors.New("x"), time.Now(), "presence-probe")
	b.metrics.PresenceProbePass.RecordPass(context.Background(), errors.New("x"), time.Now(), "presence-probe")
	assert.Equal(t, float64(2), probePassCount(t, reg, core.PassFailed))
}
