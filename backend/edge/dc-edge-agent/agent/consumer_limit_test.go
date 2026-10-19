// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/devicechain-io/dc-edge-agent/config"
)

// qos1Subscriptions is past nats-server 2.15's default of 1,000 consumers per stream, with
// margin: every QoS 1 MQTT subscription is a durable consumer on $MQTT_msgs.
const qos1Subscriptions = 1050

// subscribeQoS1 opens enough clean-session clients on addr to hold n QoS 1 subscriptions
// at once (a session's subscriptions may hold at most 65,535 un-acked messages between
// them at 1,024 each, so ~63 per session), keeps them all connected, and returns how
// many the broker granted and how many it refused with SUBACK 0x80. Any other outcome
// (no SUBACK, a transport error) fails the test, so neither count can absorb it.
func subscribeQoS1(t *testing.T, addr, prefix string, n int) (granted, refused int) {
	t.Helper()
	const perClient = 50
	var client mqtt.Client
	for i := 0; i < n; i++ {
		if i%perClient == 0 {
			client = mqttConnect(t, addr, fmt.Sprintf("%s-%d", prefix, i/perClient))
		}
		filter := fmt.Sprintf("site/%s/dev%d/cmd", prefix, i)
		err := messaging.SubscribeMqttConfirmed(client, filter, 1, func(mqtt.Client, mqtt.Message) {}, 30*time.Second)
		switch {
		case err == nil:
			granted++
		case errors.Is(err, messaging.ErrSubscriptionRefused):
			refused++
		default:
			t.Fatalf("subscription %d: %v", i, err)
		}
	}
	return granted, refused
}

// TestEdgeBrokerGrantsMoreThanAThousandQoS1Subscriptions: the agent's embedded broker
// must not cap a site at 1,000 QoS 1 subscriptions. nats-server 2.15 does by default,
// and the refusal is quiet: CONNECT succeeds and only the SUBACK carries 0x80.
func TestEdgeBrokerGrantsMoreThanAThousandQoS1Subscriptions(t *testing.T) {
	if testing.Short() {
		t.Skip("opens 1,050 JetStream consumers")
	}
	agentPort := freePort(t)
	cfg := config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     config.UplinkConfiguration{BrokerURL: "tcp://127.0.0.1:1"},
	}
	a := newAgent(t, cfg, nil)
	srv, err := a.newServer(true)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	granted, refused := subscribeQoS1(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "edge", qos1Subscriptions)
	if granted != qos1Subscriptions || refused != 0 {
		t.Fatalf("edge broker granted %d and refused %d of %d QoS 1 subscriptions; all must be granted "+
			"(nats-server 2.15 caps a stream at 1,000 consumers unless JetStreamLimits.DefaultMaxConsumers is -1)",
			granted, refused, qos1Subscriptions)
	}
}

// TestStockBrokerStillCapsQoS1Subscriptions is the negative control for the test above,
// on a broker built without the agent's options. It shows the counting can see a refusal
// at all, and it is the tripwire for the day the cap leaves nats-server's defaults: then
// this fails and the agent's DefaultMaxConsumers setting can be reconsidered.
func TestStockBrokerStillCapsQoS1Subscriptions(t *testing.T) {
	if testing.Short() {
		t.Skip("opens 1,050 JetStream consumers")
	}
	srv := newCloudBroker(t, -1, dctest.JetStreamStoreDir(t))
	t.Cleanup(srv.Shutdown)
	port := dctest.MQTTPort(t, srv)

	granted, refused := subscribeQoS1(t, fmt.Sprintf("127.0.0.1:%d", port), "stock", qos1Subscriptions)
	if granted != 1000 || refused != qos1Subscriptions-1000 {
		t.Fatalf("stock broker granted %d and refused %d of %d QoS 1 subscriptions; want 1000 granted and %d refused",
			granted, refused, qos1Subscriptions, qos1Subscriptions-1000)
	}
}
