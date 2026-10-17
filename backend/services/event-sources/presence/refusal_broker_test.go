// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"fmt"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 🔴 THE DEBOUNCE KEYS ON THE BROKER'S TEXT, SO THE BROKER IS ASKED FOR IT. A message store
// that refuses a QoS 1 publish (discard new, full, with a consumer interested in the publish)
// makes the MQTT gateway close the device's connection without a PUBACK. This drives that
// against the real broker and reads the DISCONNECT advisory through the tap's real
// subscription: its reason must be RefusalCloseReason, and the tap must hold it back. A broker
// upgrade that renames the close reason fails here instead of quietly turning the debounce off.
//
// GUARD: kills a RefusalCloseReason that does not match what the broker sends for a refused
// store write.
func TestAStoreRefusalCloseCarriesTheReasonTheTapDebounces(t *testing.T) {
	srv, mqttPort := broker(t)
	svc, err := nats.Connect(srv.ClientURL(), nats.UserInfo("dc_service", "svc"))
	require.NoError(t, err)
	t.Cleanup(svc.Close)
	js, err := svc.JetStream()
	require.NoError(t, err)
	// The store as the fail-closed reconcile declares it, declared before any MQTT client has
	// connected so the gateway keeps it: interest retention, discard new, and a bound of two.
	_, err = js.AddStream(&nats.StreamConfig{
		Name: "$MQTT_msgs", Subjects: []string{"$MQTT.msgs.>"}, Storage: nats.FileStorage,
		Retention: nats.InterestPolicy, Discard: nats.DiscardNew, MaxMsgs: 2, MaxBytes: 1 << 20,
	})
	require.NoError(t, err)
	_, err = js.AddConsumer("$MQTT_msgs", &nats.ConsumerConfig{
		Durable: "platform", FilterSubject: "$MQTT.msgs." + testInstance + ".*.devices.*.events",
		AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverNewPolicy,
	})
	require.NoError(t, err)

	emitter := newRecordingEmitter()
	deferred := prometheus.NewCounter(prometheus.CounterOpts{Name: "d"})
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true },
		Metrics{Deferred: deferred}).WithRefusalDebounce(500 * time.Millisecond)
	stop, err := tap.Subscribe(sysConn(t, srv))
	require.NoError(t, err)
	t.Cleanup(stop)

	lost := make(chan struct{})
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort))
	opts.SetClientID(testInstance + ":acme:refused")
	opts.SetUsername("dc_service")
	opts.SetPassword("svc")
	opts.SetAutoReconnect(false)
	opts.SetConnectionLostHandler(func(mqtt.Client, error) { close(lost) })
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(20*time.Second))
	require.NoError(t, tok.Error())
	emitter.await(t, "the device's connect", isDevice("acme", "refused", true))

	for i := 0; i < 5; i++ {
		cl.Publish(testInstance+"/acme/devices/refused/events", 1, false, []byte("x")).WaitTimeout(2 * time.Second)
		select {
		case <-lost:
			i = 5
		default:
		}
	}
	select {
	case <-lost:
	case <-time.After(20 * time.Second):
		t.Fatal("premise: the full store never closed the connection")
	}

	got := emitter.await(t, "the refusal-close disconnect", isDevice("acme", "refused", false))
	require.Equal(t, RefusalCloseReason, got.Event.Reason,
		"the broker's reason for a store refusal is not the one the tap debounces")
	require.Equal(t, 1.0, testutil.ToFloat64(deferred), "the refusal-close was not held back")
}
