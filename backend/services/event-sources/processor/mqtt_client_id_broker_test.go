// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoSourcesOnOneBroker starts two external sources against one real broker (an embedded
// nats-server with its MQTT listener), each under the given client id, publishes a device
// event every 100 ms for 5 s, and returns how many times each connected and how many
// messages each received.
func twoSourcesOnOneBroker(t *testing.T, idA, idB string) (connA, connB uint64, recvA, recvB int64) {
	t.Helper()
	mqttPort := dctest.FreeTCPPort(t)
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t), NoLog: true, NoSigs: true,
		ServerName: "client-id", MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "the broker did not come up")
	t.Cleanup(srv.Shutdown)

	var gotA, gotB atomic.Int64
	failsA, failsB := newFailRecorder(), newFailRecorder()
	a := newExternalMqttSourceAs(t, idA, "127.0.0.1", mqttPort, GatewayTopic(testInstance),
		func(string, []byte) { gotA.Add(1) }, failsA.fail)
	b := newExternalMqttSourceAs(t, idB, "127.0.0.1", mqttPort, GatewayTopic(testInstance),
		func(string, []byte) { gotB.Add(1) }, failsB.fail)
	require.NoError(t, startWithin(t, a, 45*time.Second))
	t.Cleanup(func() { stopWithin(t, a, 10*time.Second) })
	require.NoError(t, startWithin(t, b, 45*time.Second))
	t.Cleanup(func() { stopWithin(t, b, 10*time.Second) })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	subject := messaging.DeviceEventsSubject(testInstance, "acme", "d1")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		require.NoError(t, nc.Publish(subject, []byte(`{"device":"d1"}`)))
		_ = nc.Flush()
		time.Sleep(100 * time.Millisecond)
	}
	failsA.requireNone(t)
	failsB.requireNone(t)
	return a.connections.Load(), b.connections.Load(), gotA.Load(), gotB.Load()
}

// 🔴 THE DEFECT, ON A REAL BROKER. Two connections under one client id take the session from
// each other: the broker closes the older, paho reconnects it, and that closes the other,
// in a loop. Under ids of their own, two sources on one broker each connect once and keep
// their sessions. The ids are the ones the platform composes, so this also proves a NATS
// broker accepts them (it refuses an id containing ".").
func TestTwoExternalSourcesOnOneBrokerKeepTheirSessions(t *testing.T) {
	idA, err := ExternalMqttClientID(testInstance, "ext", "pod-a")
	require.NoError(t, err)
	idB, err := ExternalMqttClientID(testInstance, "ext", "pod-b")
	require.NoError(t, err)

	connA, connB, recvA, recvB := twoSourcesOnOneBroker(t, idA, idB)
	assert.Equal(t, uint64(1), connA, "source A was disconnected and reconnected")
	assert.Equal(t, uint64(1), connB, "source B was disconnected and reconnected")
	assert.Positive(t, recvA)
	assert.Positive(t, recvB)

	t.Run("control: one shared client id is taken over", func(t *testing.T) {
		connA, connB, _, _ := twoSourcesOnOneBroker(t, "devicechain", "devicechain")
		assert.Greater(t, connA+connB, uint64(2),
			"two sources under one id did not evict each other, so the positive above proves nothing")
	})
}
