// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// A device's QoS 1 command response, with the fail-closed store in force, reaches the
// command-responses stream's consumer ONCE, and it is the copy the store holds: the direct copy
// the broker put in the stream is skipped by the consumer (core does that for every reader of
// a direct stream), and the forwarder publishes the store copy there in its place, under the
// store's dedup id and without the gateway's header.
//
// GUARD: kills a forwarder that does not forward (the consumer would read nothing: the direct
// copy is skipped), one that forwards to the wrong subject (the device would not be the
// responder), and a consumer that reads both copies.
func TestACommandResponseReachesItsConsumerOnceFromTheStore(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
		StoreDir: dctest.JetStreamStoreDir(t), NoLog: true, NoSigs: true,
		MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1}})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	mqttPort := dctest.MQTTPort(t, srv)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	manager := func(area string, oncreate func(*messaging.NatsManager) error) *messaging.NatsManager {
		ms := &core.Microservice{InstanceId: e2eInstance, FunctionalArea: area, Readiness: core.NewReadinessGate()}
		ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(),
			Port: uint32(port), MqttFailClosed: true}
		ms.InstanceConfiguration.ApplyDefaults()
		ms.Readiness.MarkReadyWithoutAuthSurface()
		n := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), oncreate)
		n.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
			return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
				return messaging.MaxDeliveryLettered, nil
			}, nil
		})
		require.NoError(t, n.Initialize(context.Background()))
		require.NoError(t, n.Start(context.Background()))
		t.Cleanup(func() { _ = n.Stop(context.Background()) })
		return n
	}
	var writer messaging.MessageWriter
	// The from marks are recorded on the pass after the one that creates the durables, once
	// they have settled (0 here); until then the consumer would read the direct copy as well.
	messaging.SetMqttFailClosedFromSettleForTesting(t, 0)
	es := manager("event-sources", func(n *messaging.NatsManager) error {
		for i := 0; i < 2; i++ {
			if _, err := n.ReconcileMqttFailClosed(context.Background()); err != nil {
				return err
			}
		}
		w, err := n.NewWriter(streams.CommandResponses)
		writer = w
		return err
	})
	var responses messaging.MessageReader
	manager("command-delivery", func(n *messaging.NatsManager) error {
		r, err := n.NewReader(streams.CommandResponses)
		responses = r
		return err
	})

	r, err := es.NewMqttMessageStoreReader(messaging.MqttFailClosedResponses)
	require.NoError(t, err)
	t.Cleanup(r.Close)
	f := NewStoreForwarder(nil, streams.CommandResponses, r, writer)
	f.Start()
	t.Cleanup(func() { f.Stop(context.Background()) })

	clientID, err := messaging.DeviceClientID(e2eInstance, e2eTenant, "dev-1")
	require.NoError(t, err)
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)).
		SetClientID(clientID).SetConnectTimeout(5 * time.Second))
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(10*time.Second))
	require.NoError(t, tok.Error())
	t.Cleanup(func() { c.Disconnect(100) })
	pub := c.Publish(fmt.Sprintf("%s/%s/%s/dev-1", e2eInstance, e2eTenant, streams.CommandResponses), 1, false, `{"commandToken":"c1"}`)
	require.True(t, pub.WaitTimeout(10*time.Second))
	require.NoError(t, pub.Error())

	read := func(d time.Duration) (messaging.Message, error) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return responses.ReadMessage(ctx)
	}
	msg, err := read(15 * time.Second)
	require.NoError(t, err, "the response never reached its consumer")
	require.Equal(t, `{"commandToken":"c1"}`, string(msg.Value))
	device, ok := messaging.ParseDeviceFromScopedSubject(msg.Subject, streams.CommandResponses)
	require.True(t, ok)
	require.Equal(t, "dev-1", device, "the responder is the device the broker authorized")
	_, gateway := msg.Headers["Nmqtt-Pub"]
	require.False(t, gateway, "the consumer read the direct copy, not the store's")
	require.True(t, strings.HasPrefix(msg.Headers[nats.MsgIdHdr], e2eTenant+".m"), "no store dedup id: %v", msg.Headers)
	require.NoError(t, msg.Ack())
	_, err = read(2 * time.Second)
	require.ErrorIs(t, err, io.EOF, "the consumer read the response twice")
}
