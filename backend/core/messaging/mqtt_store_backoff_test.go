// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The hysteresis: on at the high-water mark, held down to the low-water mark, off below it.
//
// GUARD: kills a back-off that lifts as soon as the fill dips under the high-water mark (it
// would flap on every sample near it), and one that engages anywhere below it.
func TestTheBackoffEngagesAtTheHighWaterMarkAndLiftsBelowTheLow(t *testing.T) {
	steps := []struct {
		fill float64
		want bool
	}{
		{0, false},
		{MqttStoreBackoffLowWater, false},
		{MqttStoreBackoffHighWater - 0.01, false},
		{MqttStoreBackoffHighWater, true},
		{1, true},
		{MqttStoreBackoffHighWater - 0.01, true},
		{MqttStoreBackoffLowWater, true},
		{MqttStoreBackoffLowWater - 0.01, false},
		{MqttStoreBackoffHighWater - 0.01, false},
	}
	engaged := false
	for i, s := range steps {
		engaged = NextMqttStoreBackoff(engaged, s.fill)
		require.Equalf(t, s.want, engaged, "step %d (fill %.2f)", i, s.fill)
	}
}

// Only a store that refuses when full gives a reason to back off: a discard-old or unbounded one
// never closes a connection for being full.
func TestOnlyABoundedDiscardNewStoreIsMeasured(t *testing.T) {
	full := func(discard nats.DiscardPolicy, maxBytes, maxMsgs int64) *nats.StreamInfo {
		return &nats.StreamInfo{
			Config: nats.StreamConfig{Discard: discard, MaxBytes: maxBytes, MaxMsgs: maxMsgs},
			State:  nats.StreamState{Bytes: 90, Msgs: 9},
		}
	}
	fill, refuses := MqttStoreFill(full(nats.DiscardNew, 100, -1))
	require.True(t, refuses)
	require.InDelta(t, 0.9, fill, 1e-9)
	fill, refuses = MqttStoreFill(full(nats.DiscardNew, -1, 10))
	require.True(t, refuses, "a message bound alone is a bound")
	require.InDelta(t, 0.9, fill, 1e-9)
	_, refuses = MqttStoreFill(full(nats.DiscardOld, 100, -1))
	require.False(t, refuses, "a discard-old store evicts rather than refusing")
	_, refuses = MqttStoreFill(full(nats.DiscardNew, -1, -1))
	require.False(t, refuses, "an unbounded store never fills")
	_, refuses = MqttStoreFill(nil)
	require.False(t, refuses, "no store")
}

func backoffMetrics() *MqttStoreBackoffMetrics {
	return &MqttStoreBackoffMetrics{
		Engaged:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "e"}),
		Fill:           prometheus.NewGauge(prometheus.GaugeOpts{Name: "f"}),
		Engagements:    prometheus.NewCounter(prometheus.CounterOpts{Name: "n"}),
		SampleFailures: prometheus.NewCounter(prometheus.CounterOpts{Name: "s"}),
	}
}

// Against the real store: filled past the high-water mark the back-off engages (and says so on
// its metrics); read down below the low-water mark it lifts.
//
// GUARD: kills a back-off that never samples the store, one that reads the wrong stream or the
// wrong bound, and one that never lifts.
func TestTheBackoffFollowsTheMessageStoresFill(t *testing.T) {
	b := startGuardBroker(t)
	_, err := b.js.AddStream(guardStoreConfig(nats.DiscardNew, 10, 1))
	require.NoError(t, err)
	// Interest retention keeps a message only while a consumer is interested in it.
	require.NoError(t, guardAddDurable(b.js, guardDurable, guardFilter(guardInstance), 0))
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	m := backoffMetrics()
	bo := nmgr.StartMqttStoreBackoff(context.Background(), m, 20*time.Millisecond)
	t.Cleanup(bo.Stop)

	subject := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices.d1.events"
	for i := 0; i < 8; i++ {
		_, err := b.js.Publish(subject, []byte(fmt.Sprintf("m%d", i)))
		require.NoError(t, err)
	}
	require.Eventually(t, bo.Engaged, 5*time.Second, 10*time.Millisecond, "8 of 10 is the high-water mark")
	require.Equal(t, 1.0, testutil.ToFloat64(m.Engaged))
	require.Equal(t, 1.0, testutil.ToFloat64(m.Engagements))
	require.InDelta(t, 0.8, testutil.ToFloat64(m.Fill), 1e-9)

	// Read three of them: 5 of 10 is below the low-water mark.
	sub, err := b.js.PullSubscribe(guardFilter(guardInstance), guardDurable, nats.Bind(MqttMessageStore, guardDurable))
	require.NoError(t, err)
	msgs, err := sub.Fetch(3, nats.MaxWait(5*time.Second))
	require.NoError(t, err)
	for _, msg := range msgs {
		require.NoError(t, msg.AckSync())
	}
	require.Eventually(t, func() bool { return !bo.Engaged() }, 5*time.Second, 10*time.Millisecond,
		"the back-off was not lifted below the low-water mark")
	require.Equal(t, 0.0, testutil.ToFloat64(m.Engaged))
}

// A store that is not fail-closed (the gateway's own, discard old) never backs devices off,
// however full.
func TestADiscardOldStoreNeverBacksDevicesOff(t *testing.T) {
	b := startGuardBroker(t)
	_, err := b.js.AddStream(guardStoreConfig(nats.DiscardOld, 10, 1))
	require.NoError(t, err)
	require.NoError(t, guardAddDurable(b.js, guardDurable, guardFilter(guardInstance), 0))
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	bo := nmgr.StartMqttStoreBackoff(context.Background(), nil, 20*time.Millisecond)
	t.Cleanup(bo.Stop)
	subject := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices.d1.events"
	for i := 0; i < 10; i++ {
		_, err := b.js.Publish(subject, []byte("x"))
		require.NoError(t, err)
	}
	require.Never(t, bo.Engaged, 300*time.Millisecond, 20*time.Millisecond)
}

// A back-off whose samples stop reading the store lifts once the fill has gone unread for
// mqttStoreBackoffStale: refusing every device on a failed sample is an outage of its own.
//
// GUARD: kills a back-off held on a reading it can no longer renew.
func TestAnUnreadableStoreLiftsTheBackoff(t *testing.T) {
	saved := mqttStoreBackoffStale
	mqttStoreBackoffStale = 200 * time.Millisecond
	t.Cleanup(func() { mqttStoreBackoffStale = saved })
	b := startGuardBroker(t)
	_, err := b.js.AddStream(guardStoreConfig(nats.DiscardNew, 10, 1))
	require.NoError(t, err)
	require.NoError(t, guardAddDurable(b.js, guardDurable, guardFilter(guardInstance), 0))
	subject := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices.d1.events"
	for i := 0; i < 9; i++ {
		_, err := b.js.Publish(subject, []byte("x"))
		require.NoError(t, err)
	}
	// Its own connection, so closing it makes every later sample fail.
	nc, err := nats.Connect(b.srv.ClientURL())
	require.NoError(t, err)
	js, err := nc.JetStream()
	require.NoError(t, err)
	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	m := backoffMetrics()
	bo := nmgr.StartMqttStoreBackoff(context.Background(), m, 20*time.Millisecond)
	t.Cleanup(bo.Stop)
	require.Eventually(t, bo.Engaged, 5*time.Second, 10*time.Millisecond, "premise: engaged")
	nc.Close()
	require.Eventually(t, func() bool { return !bo.Engaged() }, 5*time.Second, 10*time.Millisecond,
		"a back-off that cannot read the store any more stayed engaged")
	require.Positive(t, testutil.ToFloat64(m.SampleFailures))
}
