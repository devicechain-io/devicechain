// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A finish held by an unsettled cluster is a run: it is measured from its first held pass,
// restarts after a gap of passes, and ends when a pass is not held.
//
// GUARD: kills a hold that restarts on every pass, one that never ends, and one that keeps
// reporting a hold after the passes stopped.
func TestAHeldFinishIsMeasuredFromItsFirstHeldPass(t *testing.T) {
	var h mqttFinishHold
	t0 := time.Unix(1_000_000, 0)
	require.Zero(t, h.held(t0))
	require.Equal(t, 90*time.Second, h.held(t0.Add(90*time.Second)))
	require.Equal(t, 100*time.Second, h.heldFor(t0.Add(100*time.Second)))
	require.Zero(t, h.heldFor(t0.Add(90*time.Second+4*MqttFailClosedWatchInterval)), "no pass for several intervals")
	require.Zero(t, h.held(t0.Add(time.Hour)), "a gap starts a new run")
	h.clear()
	require.Zero(t, h.heldFor(t0.Add(time.Hour)))
}

// The bound watch exports how long the finish has been held, so a pod with the fail-closed
// setting off (the one that finishes a release) shows it.
//
// GUARD: kills a gauge that is never set and one that is not zero when nothing is held.
func TestTheBoundWatchExportsHowLongTheFinishIsHeld(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, false)
	m := &MqttStoreBoundMetrics{
		unbounded:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "u"}, []string{"stream"}),
		boundedLate: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "l"}, []string{"stream"}),
		finishHeld:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "h"}),
	}
	nmgr.checkMqttStoreBounds(context.Background(), m, nil)
	require.Zero(t, testutil.ToFloat64(m.finishHeld))

	nmgr.mqttFinish.mu.Lock()
	nmgr.mqttFinish.since = time.Now().Add(-time.Minute)
	nmgr.mqttFinish.last = time.Now()
	nmgr.mqttFinish.mu.Unlock()
	nmgr.checkMqttStoreBounds(context.Background(), m, nil)
	require.InDelta(t, 60, testutil.ToFloat64(m.finishHeld), 5)
}
