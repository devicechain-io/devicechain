// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/streams"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// THE GATEWAY NAMESPACE, AGAINST A REAL BROKER WITH THE AUTH CALLOUT CONFIGURED.
//
// The earlier broker rigs ran without auth_callout, which is the production feature that mints
// every device's grant: a grant bug is invisible to them. Here a real nats-server is configured
// the way the deployed one is, the callout responder mints each device's JWT from the token it
// authenticated, and devices connect over MQTT and publish. What reaches the device-events
// capture stream (declared with its real subject list) is what the platform would ingest.

const nsInstance = "inst-1"

// connectAs opens a device's MQTT session through the callout and returns the live client.
func connectAs(t *testing.T, port int, token, credentialId string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port))
	// The plain id the callout requires (a literal-shaped value from core, as the platform issues it).
	clientID, err := messaging.DeviceClientID(nsInstance, "acme", token)
	require.NoError(t, err)
	opts.SetClientID(clientID)
	opts.SetUsername("acme:" + credentialId)
	opts.SetConnectRetry(false)
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second), "%s: connect did not settle", token)
	require.NoError(t, tok.Error(), "%s: the broker refused the connect", token)
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

// publish sends a QoS 1 publish and waits for its PUBACK, which a broker gives a permission
// violation too (MQTT 3.1.1 cannot refuse a publish): the ack proves nothing about delivery.
func publish(t *testing.T, c mqtt.Client, topic string) {
	t.Helper()
	tok := c.Publish(topic, 1, false, []byte(`{"v":1}`))
	require.True(t, tok.WaitTimeout(10*time.Second), "publish to %s did not settle", topic)
	require.NoError(t, tok.Error())
}

// captured polls the capture stream until it holds want (as a sorted subject list), then waits a
// little longer and re-reads, so a forbidden publish that was merely slow would still show up.
func captured(t *testing.T, js nats.JetStreamContext, stream string, want []string) []string {
	t.Helper()
	read := func() []string {
		info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: ">"})
		require.NoError(t, err)
		var got []string
		for subject := range info.State.Subjects {
			got = append(got, subject)
		}
		sort.Strings(got)
		return got
	}
	sort.Strings(want)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got := read(); fmt.Sprint(got) == fmt.Sprint(want) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	return read()
}

func TestAGatewayPublishesOnlyOnItsOwnNamespaceAgainstARealCalloutBroker(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	nc, mqttPort := startCalloutBroker(t, creds)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// The capture stream, with the subjects the platform declares for it.
	const stream = "CAPTURE_NS"
	_, err = js.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: messaging.StreamSubjects(nsInstance, streams.DeviceEventsCapture),
		Storage: nats.MemoryStorage,
	})
	require.NoError(t, err)

	// Credential id == device token, so the fake resolves whoever presents it.
	api := fakeAuthApi{authFn: func(_ context.Context, p *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = p.CredentialId
		return d, nil
	}}
	r := mustResponder(t, nc, api, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	require.NoError(t, r.Start())
	t.Cleanup(func() { _ = r.Stop() })

	gw := connectAs(t, mqttPort, "gw", "gw")
	gw2 := connectAs(t, mqttPort, "gw2", "gw2")
	plain := connectAs(t, mqttPort, "plain", "plain")

	topic := func(tenant, gateway, child, leaf string) string {
		return messaging.SubjectToMqttTopic(messaging.GatewayDeviceSubject(nsInstance, tenant, gateway, child, leaf))
	}

	// What each login tries, and the one thing the broker lets through for each.
	publish(t, gw, topic("acme", "gw", "c1", "events"))       // its own namespace: captured
	publish(t, gw, topic("acme", "gw", "c2", "events"))       // ANY child token under it: captured (the grant is O(1))
	publish(t, gw, topic("acme", "gw2", "c1", "events"))      // ANOTHER gateway's namespace: dropped
	publish(t, gw, topic("other", "gw", "c1", "events"))      // ANOTHER TENANT's namespace: dropped
	publish(t, gw, "inst-1/acme/devices/c1/events")           // a child's own topic: dropped (not gw's device)
	publish(t, gw2, topic("acme", "gw", "c9", "events"))      // gw2 into gw's namespace: dropped
	publish(t, plain, topic("acme", "gw", "c1", "events"))    // a non-gateway device into gw's: dropped
	publish(t, plain, "inst-1/acme/devices/plain/events")     // its own telemetry: captured (control)
	publish(t, plain, topic("acme", "plain", "c1", "events")) // a device's own gateway namespace is granted to EVERY device: captured here, refused by the resolver unless assigned

	want := []string{
		"inst-1.acme.devices.plain.events",
		"inst-1.acme.gateways.gw.devices.c1.events",
		"inst-1.acme.gateways.gw.devices.c2.events",
		"inst-1.acme.gateways.plain.devices.c1.events",
	}
	got := captured(t, js, stream, append([]string(nil), want...))
	require.Equal(t, want, got,
		"the capture stream holds exactly what each login's own grant allowed; anything else is a grant that reaches too far (or too little)")

	// The captured subject carries the broker-asserted publisher: for plain's publish that is
	// plain, not whichever device it named, and the resolver (gateway_refusal.go) decides from there.
	gateway, child, ok := messaging.ParseDeviceEventsSubject("inst-1.acme.gateways.plain.devices.c1.events")
	require.True(t, ok)
	require.Equal(t, [2]string{"plain", "c1"}, [2]string{gateway, child})

	// Notices: a gateway hears its own, and only its own.
	got1 := make(chan string, 4)
	got2 := make(chan string, 4)
	// SubscribeMqttConfirmed reads the granted QoS: paho reports a SUBACK 0x80 as success.
	sub := func(c mqtt.Client, gateway string, ch chan string) error {
		return messaging.SubscribeMqttConfirmed(c,
			messaging.SubjectToMqttTopic(messaging.GatewayNoticesSubject(nsInstance, "acme", gateway)), 0,
			func(_ mqtt.Client, m mqtt.Message) { ch <- string(m.Payload()) }, 10*time.Second)
	}
	require.NoError(t, sub(gw, "gw", got1), "a gateway was refused its own notices subject")
	require.NoError(t, sub(gw2, "gw2", got2))
	require.ErrorIs(t, sub(gw2, "gw", got2), messaging.ErrSubscriptionRefused,
		"a gateway was allowed to subscribe to ANOTHER gateway's notices")

	notices := messaging.NewGatewayNoticePublisher(func() *nats.Conn { return nc }, nsInstance)
	require.NoError(t, notices.Notice("acme", "gw", messaging.GatewayNotice{AltId: "a1", Device: "dev", Reason: messaging.GatewayNoticeReasonNotGatewayChild}))
	select {
	case body := <-got1:
		require.JSONEq(t, `{"altId":"a1","device":"dev","reason":"not_gateway_child"}`, body)
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway never received its notice")
	}
	select {
	case body := <-got2:
		t.Fatalf("gw2 received a notice meant for gw: %s", body)
	case <-time.After(500 * time.Millisecond):
	}

	// A device cannot forge a notice either: the notices subject is the platform's word.
	publish(t, gw2, messaging.SubjectToMqttTopic(messaging.GatewayNoticesSubject(nsInstance, "acme", "gw")))
	select {
	case body := <-got1:
		t.Fatalf("a device published a notice to a gateway: %s", body)
	case <-time.After(500 * time.Millisecond):
	}
}
