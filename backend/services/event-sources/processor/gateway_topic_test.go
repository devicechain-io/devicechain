// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstance = "inst-1"

// startGateway runs an embedded NATS server with the MQTT gateway enabled and
// returns a NATS connection plus the gateway's MQTT port.
//
// A REAL gateway is what makes this test worth having. The claim under test is
// about how the broker matches a topic filter against the subjects the platform
// publishes on, and that is the broker's behaviour, not ours — asserted against a
// hand-rolled matcher it would only confirm that our idea of MQTT wildcards
// agrees with itself. The bug this pins was invisible to exactly that kind of
// test for as long as it existed.
func startGateway(t *testing.T) (*nats.Conn, int) {
	t.Helper()

	// The gateway needs a fixed port: the server exposes no accessor for an
	// ephemerally-bound MQTT listener the way it does for the client one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "pick a free port")
	mqttPort := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true, // the MQTT gateway is built on JetStream
		StoreDir:   t.TempDir(),
		ServerName: "gateway-test",
		MQTT:       natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	return nc, mqttPort
}

// subscribeGateway connects an MQTT client to the gateway and subscribes with the
// production topic filter, returning a channel of delivered topics.
func subscribeGateway(t *testing.T, mqttPort int) <-chan string {
	t.Helper()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort))
	opts.SetClientID("gateway-test-sub")
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second), "mqtt connect timed out")
	require.NoError(t, tok.Error())
	t.Cleanup(func() { cl.Disconnect(100) })

	delivered := make(chan string, 64)
	sub := cl.Subscribe(GatewayTopic(testInstance), 1, func(_ mqtt.Client, m mqtt.Message) {
		delivered <- m.Topic()
	})
	require.True(t, sub.WaitTimeout(15*time.Second), "mqtt subscribe timed out")
	require.NoError(t, sub.Error())

	return delivered
}

// The gateway subscription must not deliver ANY subject the platform publishes
// internally, for every stream in the declaration — not a sample of them.
//
// Iterating streams.All rather than listing suffixes is the point: a suffix added
// later is covered automatically. The defect this replaces was a denylist that
// named two suffixes and stayed silent about the rest, so a test that also had to
// be remembered would reproduce the original failure mode rather than close it.
//
// The canonical device-events subject is published LAST and expected FIRST: NATS
// preserves publish order from a single connection, so if any internal subject
// leaked it arrives ahead of the canonical one and is reported as the failure.
func TestGatewaySubscriptionExcludesInternalSubjects(t *testing.T) {
	nc, mqttPort := startGateway(t)
	delivered := subscribeGateway(t, mqttPort)

	const tenant = "acme"
	const device = "sensor-001"

	published := make([]string, 0, len(streams.All))
	for _, s := range streams.All {
		// A device-events capture stream is not INTERNAL traffic — its subject is
		// the telemetry subject this subscription exists to receive. Publishing it
		// here would assert the opposite of the truth and fail. The canonical
		// publish below is what covers it.
		if s.Shape == streams.ShapeDeviceEvents {
			continue
		}
		subject := messaging.ConcreteSubjectFor(testInstance, tenant, s.Suffix, device)
		require.NoError(t, nc.Publish(subject, []byte(`{"internal":true}`)))
		published = append(published, subject)
	}
	require.NotEmpty(t, published, "stream declaration is empty — the test would assert nothing")

	canonical := messaging.DeviceEventsSubject(testInstance, tenant, device)
	require.NoError(t, nc.Publish(canonical, []byte(`{"telemetry":true}`)))
	require.NoError(t, nc.Flush())

	select {
	case got := <-delivered:
		// require, not assert: on a real leak the offending subject arrives FIRST
		// (publish order is preserved on one connection). Continuing would consume
		// the canonical message in the drain check below and report it as the
		// offender, burying the true diagnostic under a misleading one.
		require.Equal(t, messaging.SubjectToMqttTopic(canonical), got,
			"an internal subject was delivered to the gateway subscription; it would be metered against the tenant's ingest budget a second time")
	case <-time.After(10 * time.Second):
		t.Fatalf("device telemetry on %q was not delivered to %q — the gateway ingests nothing",
			canonical, GatewayTopic(testInstance))
	}

	// Nothing may follow it either: the ordering check above catches a leak that
	// arrives early, this catches one that arrives late.
	select {
	case extra := <-delivered:
		t.Fatalf("gateway subscription also delivered %q; only device events may reach it", extra)
	case <-time.After(500 * time.Millisecond):
	}
}

// Another instance's device traffic must not reach this instance's subscription,
// which is what keeps two instances isolated on a shared broker (ADR-048).
func TestGatewaySubscriptionIsInstanceScoped(t *testing.T) {
	nc, mqttPort := startGateway(t)
	delivered := subscribeGateway(t, mqttPort)

	other := messaging.DeviceEventsSubject("inst-2", "acme", "sensor-001")
	require.NoError(t, nc.Publish(other, []byte(`{"telemetry":true}`)))
	mine := messaging.DeviceEventsSubject(testInstance, "acme", "sensor-001")
	require.NoError(t, nc.Publish(mine, []byte(`{"telemetry":true}`)))
	require.NoError(t, nc.Flush())

	select {
	case got := <-delivered:
		require.Equal(t, messaging.SubjectToMqttTopic(mine), got,
			"another instance's device traffic reached this instance's gateway subscription")
	case <-time.After(10 * time.Second):
		t.Fatal("own-instance telemetry was not delivered")
	}
}

// A topic the gateway delivers must be one deviceFromTopic can read a device token
// from. If the two ever disagree, every ingested event silently loses its
// transport-asserted device identity and the body-vs-transport check in the decode
// worker stops running — a check that cannot fail is worse than no check.
func TestDeliveredTopicYieldsDeviceToken(t *testing.T) {
	topic := messaging.SubjectToMqttTopic(
		messaging.DeviceEventsSubject(testInstance, "acme", "sensor-001"))

	assert.Equal(t, "sensor-001", deviceFromTopic(topic))
}
