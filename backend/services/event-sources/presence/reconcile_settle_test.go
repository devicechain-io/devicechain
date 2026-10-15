// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// settleRig drives Run over a projection the test moves by hand between passes, which is
// what makes a repair's fate visible: a projection that does not move is a repair that was
// refused downstream.
type settleRig struct {
	r         *Reconciler
	reads     fakeProjection
	repaired  *prometheus.CounterVec
	unapplied *prometheus.CounterVec
}

func newSettleRig(t *testing.T, live []LiveDevice, stored ...StoredDevice) *settleRig {
	t.Helper()
	conns := make([]connzConn, 0, len(live))
	for _, d := range live {
		conns = append(conns, liveConn(t, d))
	}
	rig := &settleRig{
		reads:     fakeProjection{states: map[string]map[string]StoredDevice{"acme": projection(stored...)}},
		repaired:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_repaired_total"}, []string{"direction"}),
		unapplied: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_unapplied_total"}, []string{"direction"}),
	}
	tap := NewTap(testInstance, "mqtt1", newRecordingEmitter(), func(string, string, time.Time, bool) bool { return true }, Metrics{})
	requests := &fakeRequester{servers: []string{"A"}, claimed: 1, conns: map[string][]connzConn{"A": conns}}
	rig.r = NewReconciler(tap, requests, fakeTenants{[]string{"acme"}}, rig.reads,
		ReconcileMetrics{Repaired: rig.repaired, Unapplied: rig.unapplied}, time.Second, time.Now, DefaultPassTimeout)
	return rig
}

func (s *settleRig) pass(t *testing.T) {
	t.Helper()
	require.NoError(t, s.r.Run(t.Context()))
}

func (s *settleRig) counts(direction string) (repaired, unapplied float64) {
	return testutil.ToFloat64(s.repaired.WithLabelValues(direction)), testutil.ToFloat64(s.unapplied.WithLabelValues(direction))
}

// 🔴 A REFUSED REPAIR IS NOT A REPAIR. A device the projection holds online and the broker
// does not is repaired with a DISCONNECTED on every pass. When the resolver refuses that
// repair — as it once refused every presence transition of a disabled device — the publish
// still succeeds, and counting the publish reported the device repaired every five minutes
// while it read online forever. The repair counts only once the projection shows it.
func TestARefusedRepairIsNotCountedAsRepaired(t *testing.T) {
	gone := StoredDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575, Active: true}
	rig := newSettleRig(t, nil, gone)

	rig.pass(t) // emits the DISCONNECTED; the projection never moves
	repaired, unapplied := rig.counts("disconnect")
	require.Equal(t, 0.0, repaired, "a repair was counted before anything could have applied it")
	require.Equal(t, 0.0, unapplied)

	rig.pass(t)
	repaired, unapplied = rig.counts("disconnect")
	require.Equal(t, 0.0, repaired, "a repair the projection never applied was counted as repaired")
	require.Equal(t, 1.0, unapplied, "a repair the projection never applied was not reported as unapplied")

	rig.pass(t) // emitted again, refused again: counted again, once per pass
	repaired, unapplied = rig.counts("disconnect")
	require.Equal(t, 0.0, repaired)
	require.Equal(t, 2.0, unapplied)
}

// The counterweight: a repair the projection applied is counted as repaired, once, and not
// as unapplied.
func TestAnAppliedRepairIsCountedOnce(t *testing.T) {
	gone := StoredDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575, Active: true}
	rig := newSettleRig(t, nil, gone)

	rig.pass(t)
	applied := gone
	applied.Active = false
	rig.reads.states["acme"][DeviceKey("acme", "sensor-001")] = applied
	rig.pass(t)
	rig.pass(t)

	repaired, unapplied := rig.counts("disconnect")
	require.Equal(t, 1.0, repaired, "an applied repair was not counted, or was counted twice")
	require.Equal(t, 0.0, unapplied, "an applied repair was reported as unapplied")
}

// The connect direction settles the same way: a live device the projection never learns
// about is a refused repair, and one it learns about on the repair's session is applied.
func TestAConnectRepairIsSettledByTheNextRead(t *testing.T) {
	live := LiveDevice{Tenant: "acme", DeviceToken: "sensor-001", SessionId: 1786552664076882575}

	refused := newSettleRig(t, []LiveDevice{live})
	refused.pass(t)
	refused.pass(t)
	repaired, unapplied := refused.counts("connect")
	require.Equal(t, 0.0, repaired, "a connect repair the projection never applied was counted as repaired")
	require.Equal(t, 1.0, unapplied)

	landed := newSettleRig(t, []LiveDevice{live})
	landed.pass(t)
	landed.reads.states["acme"][DeviceKey("acme", "sensor-001")] = believedOnline(live)
	landed.pass(t)
	repaired, unapplied = landed.counts("connect")
	require.Equal(t, 1.0, repaired, "an applied connect repair was not counted")
	require.Equal(t, 0.0, unapplied)
}

// liveConn is the broker's CONNZ entry for a live device; the session id is the
// connection's start, as the inventory reads it.
func liveConn(t *testing.T, d LiveDevice) connzConn {
	t.Helper()
	return connzConn{MQTTClient: testInstance + ":" + d.Tenant + ":" + d.DeviceToken, Type: "mqtt", Start: startAt(int64(d.SessionId))}
}
