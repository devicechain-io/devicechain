// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// mqttGatewayBroker is a single embedded broker with the REAL MQTT gateway on, so the
// gateway's own lazily-created streams exist (or not) exactly as in a cluster.
func mqttGatewayBroker(t *testing.T) (nmgr *NatsManager, mqttURL string) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
		MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return failClosedManager(t, nc, js, guardInstance, false), fmt.Sprintf("tcp://127.0.0.1:%d", dctest.MQTTPort(t, srv))
}

// connectMqttDevice makes the gateway create its streams, as the first device does.
func connectMqttDevice(t *testing.T, url string) {
	t.Helper()
	clientID, err := DeviceClientID(guardInstance, guardTenant, "late1")
	require.NoError(t, err)
	g, err := tryConnectGuardClient(t, url, clientID, mqtt.NewMemoryStore(), false)
	require.NoError(t, err)
	tok := g.c.Publish(guardTopic(guardInstance, guardTenant, "late1"), 1, false, "x")
	require.True(t, tok.WaitTimeout(20*time.Second))
	require.NoError(t, tok.Error())
}

func mqttMaxBytes(t *testing.T, nmgr *NatsManager, name string) int64 {
	t.Helper()
	info, err := nmgr.js.StreamInfo(name)
	require.NoError(t, err, name)
	return info.Config.MaxBytes
}

const lateMsgs, lateQoS2 = int64(256 << 20), int64(64 << 20)

// 🔴 THE FRESH-INSTALL ORDER. event-sources reconciles at startup, before any device has
// connected, so the gateway has created nothing to bound; the streams appear on the first
// MQTT connection. Nothing may leave them unbounded until some later restart.
func TestGatewayStreamsCreatedAfterStartupReconcileAreBoundedWithoutARestart(t *testing.T) {
	fastMqttStoreLookup(t)
	nmgr, url := mqttGatewayBroker(t)

	require.NoError(t, nmgr.ReconcileMqttStores(context.Background(), lateMsgs, lateQoS2))
	_, err := nmgr.js.StreamInfo(MqttMessageStore)
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "premise: nothing has connected yet")

	connectMqttDevice(t, url)
	require.EqualValues(t, -1, mqttMaxBytes(t, nmgr, MqttMessageStore), "premise: the gateway creates it unbounded")

	m := newMqttStoreBoundMetricsForTest(t)
	w := nmgr.StartMqttStoreBoundWatch(context.Background(), m, 50*time.Millisecond, lateMsgs, lateQoS2)
	t.Cleanup(w.Stop)
	require.Eventually(t, func() bool {
		return mqttMaxBytes(t, nmgr, MqttMessageStore) == lateMsgs &&
			mqttMaxBytes(t, nmgr, MqttPubRelStore) == lateQoS2 &&
			mqttMaxBytes(t, nmgr, MqttQoS2InStore) == lateQoS2
	}, 10*time.Second, 50*time.Millisecond, "gateway streams must be bounded without a restart")
}

func newMqttStoreBoundMetricsForTest(t *testing.T) *MqttStoreBoundMetrics {
	t.Helper()
	return NewMqttStoreBoundMetrics(&core.Microservice{InstanceId: "test", FunctionalArea: "event-sources"})
}

func allBounded(t *testing.T, nmgr *NatsManager) bool {
	return mqttMaxBytes(t, nmgr, MqttMessageStore) == lateMsgs &&
		mqttMaxBytes(t, nmgr, MqttPubRelStore) == lateQoS2 &&
		mqttMaxBytes(t, nmgr, MqttQoS2InStore) == lateQoS2
}

// The other order: the gateway already made its streams when the service starts. The startup
// reconcile bounds them and the watch must leave them alone (no update, no late count).
func TestGatewayStreamsThatExistAtStartupAreBoundedOnceAndNotCountedLate(t *testing.T) {
	fastMqttStoreLookup(t)
	nmgr, url := mqttGatewayBroker(t)
	connectMqttDevice(t, url)
	require.NoError(t, nmgr.ReconcileMqttStores(context.Background(), lateMsgs, lateQoS2))
	require.True(t, allBounded(t, nmgr))

	m := newMqttStoreBoundMetricsForTest(t)
	nmgr.checkMqttStoreBounds(context.Background(), m, mqttWatchBounds(lateMsgs, lateQoS2))
	require.True(t, allBounded(t, nmgr))
	for _, s := range mqttGatewayStreams {
		require.EqualValues(t, 0, testutil.ToFloat64(m.boundedLate.WithLabelValues(s)), s)
		require.EqualValues(t, 0, testutil.ToFloat64(m.unbounded.WithLabelValues(s)), s)
	}
}

// A late bound is counted, and a stream the gateway recreates unbounded is bounded again.
func TestLateBoundIsCountedAndARecreatedStreamIsBoundedAgain(t *testing.T) {
	nmgr, url := mqttGatewayBroker(t)
	connectMqttDevice(t, url)
	m := newMqttStoreBoundMetricsForTest(t)
	bounds := mqttWatchBounds(lateMsgs, lateQoS2)

	nmgr.checkMqttStoreBounds(context.Background(), m, bounds)
	require.True(t, allBounded(t, nmgr))
	for _, s := range mqttGatewayStreams {
		require.EqualValues(t, 1, testutil.ToFloat64(m.boundedLate.WithLabelValues(s)), s)
	}

	info, err := nmgr.js.StreamInfo(MqttQoS2InStore)
	require.NoError(t, err)
	cfg := info.Config
	cfg.MaxBytes = -1
	_, err = nmgr.js.UpdateStream(&cfg)
	require.NoError(t, err)
	nmgr.checkMqttStoreBounds(context.Background(), m, bounds)
	require.EqualValues(t, lateQoS2, mqttMaxBytes(t, nmgr, MqttQoS2InStore))
	require.EqualValues(t, 2, testutil.ToFloat64(m.boundedLate.WithLabelValues(MqttQoS2InStore)))
}

// With the fail-closed setting on, $MQTT_msgs is declared discard-new at its own bound; the
// watch is told msgs=0 and must neither touch it nor fight it, while still bounding the
// QoS 2 stores.
func TestWatchLeavesTheFailClosedMessageStoreAlone(t *testing.T) {
	nmgr, url := mqttGatewayBroker(t)
	connectMqttDevice(t, url)
	info, err := nmgr.js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	cfg := info.Config
	cfg.MaxBytes, cfg.Discard = 100<<20, nats.DiscardNew
	_, err = nmgr.js.UpdateStream(&cfg)
	require.NoError(t, err)

	nmgr.checkMqttStoreBounds(context.Background(), nil, mqttWatchBounds(0, lateQoS2))
	info, err = nmgr.js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.EqualValues(t, 100<<20, info.Config.MaxBytes)
	require.Equal(t, nats.DiscardNew, info.Config.Discard)
	require.EqualValues(t, lateQoS2, mqttMaxBytes(t, nmgr, MqttPubRelStore))
	require.EqualValues(t, lateQoS2, mqttMaxBytes(t, nmgr, MqttQoS2InStore))
}

// A ceiling that cannot be set leaves the stream unbounded and says so on the gauge, which is
// what the alert reads; a stream that does not exist yet is not a problem.
func TestUnboundableStreamRaisesTheUnboundedGauge(t *testing.T) {
	nmgr, url := mqttGatewayBroker(t)
	m := newMqttStoreBoundMetricsForTest(t)
	nmgr.checkMqttStoreBounds(context.Background(), m, mqttWatchBounds(lateMsgs, lateQoS2))
	for _, s := range mqttGatewayStreams {
		require.EqualValues(t, 0, testutil.ToFloat64(m.unbounded.WithLabelValues(s)), "absent: "+s)
	}
	connectMqttDevice(t, url)
	nmgr.checkMqttStoreBounds(context.Background(), m, mqttWatchBounds(1<<60, 1<<60))
	for _, s := range mqttGatewayStreams {
		require.EqualValues(t, 1, testutil.ToFloat64(m.unbounded.WithLabelValues(s)), s)
		require.EqualValues(t, -1, mqttMaxBytes(t, nmgr, s), s)
	}
	nmgr.checkMqttStoreBounds(context.Background(), m, mqttWatchBounds(lateMsgs, lateQoS2))
	for _, s := range mqttGatewayStreams {
		require.EqualValues(t, 0, testutil.ToFloat64(m.unbounded.WithLabelValues(s)), s)
	}
}

// Never write 0 (UNLIMITED to JetStream).
func TestWatchBoundsNeverCarryANonPositiveCeiling(t *testing.T) {
	for _, b := range mqttWatchBounds(0, lateQoS2) {
		require.Positive(t, b.MaxBytes, b.Name)
		require.NotEqual(t, MqttMessageStore, b.Name)
	}
	require.Empty(t, mqttWatchBounds(0, 0))
}

// NEGATIVE CONTROL: the startup reconcile alone (what main did) leaves the streams unbounded
// after the first device connects; only the watch bounds them. If this ever fails, the
// positive test above proves nothing.
func TestNegativeControlStartupReconcileAloneLeavesLateStreamsUnbounded(t *testing.T) {
	fastMqttStoreLookup(t)
	nmgr, url := mqttGatewayBroker(t)
	require.NoError(t, nmgr.ReconcileMqttStores(context.Background(), lateMsgs, lateQoS2))
	connectMqttDevice(t, url)
	time.Sleep(2 * time.Second)
	require.EqualValues(t, -1, mqttMaxBytes(t, nmgr, MqttMessageStore))
}
