// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
)

// fakeMqttMessage is a minimal mqtt.Message for exercising onMessage without a
// live broker: only Topic and Payload are consulted by the receive path.
type fakeMqttMessage struct {
	topic   string
	payload []byte
}

func (m *fakeMqttMessage) Duplicate() bool   { return false }
func (m *fakeMqttMessage) Qos() byte         { return 0 }
func (m *fakeMqttMessage) Retained() bool    { return false }
func (m *fakeMqttMessage) Topic() string     { return m.topic }
func (m *fakeMqttMessage) MessageID() uint16 { return 0 }
func (m *fakeMqttMessage) Payload() []byte   { return m.payload }
func (m *fakeMqttMessage) Ack()              {}

// newTestMqttSource builds an MQTT source with a buffered message channel (no
// decode workers drain it, so the test inspects what onMessage enqueued) and the
// given allow gate. received counts are captured.
//
// These tests call onMessage DIRECTLY, so they exercise topic shapes the gateway
// subscription would never deliver — which is the external-broker source's case,
// where the topic is operator-configured and arbitrary. What the gateway itself
// receives is a property of the broker, and is pinned against a live MQTT gateway
// in gateway_topic_test.go rather than asserted here.
func newTestMqttSource(t *testing.T, allow func(string, string) bool) (*MqttEventSource, *int) {
	t.Helper()
	receivedCount := 0
	es, err := NewMqttEventSource("mqtt-test", map[string]string{"host": "h", "port": "1883", "topic": GatewayTopic("inst-1")},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) { receivedCount++ },
		func(string, string, *model.UnresolvedEvent, interface{}) {},
		func(string, string, []byte, error) {},
		allow)
	assert.NoError(t, err)
	es.messages = make(chan rawMessage, 8)
	return es, &receivedCount
}

// A message on a well-formed topic whose tenant is within its limit is enqueued
// for decode and counted as received.
func TestMqttOnMessage_Allowed(t *testing.T) {
	es, received := newTestMqttSource(t, func(string, string) bool { return true })

	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/acme/events", payload: []byte(`{"device":"d1"}`)})

	assert.Len(t, es.messages, 1)
	msg := <-es.messages
	assert.Equal(t, "acme", msg.tenant)
	assert.Equal(t, 1, *received)
}

// A message whose tenant is over its limit is shed: nothing is enqueued and it is
// not counted as received (accounting happens after the gate).
func TestMqttOnMessage_RateLimited(t *testing.T) {
	es, received := newTestMqttSource(t, func(string, string) bool { return false })

	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/acme/events", payload: []byte(`{"device":"d1"}`)})

	assert.Len(t, es.messages, 0, "shed message must not be enqueued")
	assert.Equal(t, 0, *received, "shed message must not be counted as received")
}

// A topic segment that is not a valid tenant token is dropped fail-closed before
// it can seed a limiter bucket — the allow gate is never even consulted.
func TestMqttOnMessage_InvalidTenantDropped(t *testing.T) {
	allowCalls := 0
	es, _ := newTestMqttSource(t, func(string, string) bool { allowCalls++; return true })

	// A space is outside the tenant token grammar (core.ValidateToken).
	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/bad tenant/events", payload: []byte(`{}`)})

	assert.Len(t, es.messages, 0)
	assert.Equal(t, 0, allowCalls, "invalid tenant must be dropped before metering")
}

// A topic with no parseable tenant segment is dropped and never metered.
func TestMqttOnMessage_NoTenantDropped(t *testing.T) {
	allowCalls := 0
	es, _ := newTestMqttSource(t, func(string, string) bool { allowCalls++; return true })

	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1", payload: []byte(`{}`)})

	assert.Len(t, es.messages, 0)
	assert.Equal(t, 0, allowCalls)
}

// Command traffic shares the {instanceId}/{tenant}/… tree this source subscribes
// to with a wildcard, but it is not telemetry. The defect this pins is not the
// noise — it is that metering ran first, so every command the platform sent and
// every response a device returned spent the tenant's INGEST budget. A busy
// command session could rate-limit that tenant's real events.
func TestMqttOnMessage_CommandPlaneIgnored(t *testing.T) {
	for _, topic := range []string{
		"inst-1/acme/device-commands",
		"inst-1/acme/command-responses",
	} {
		t.Run(topic, func(t *testing.T) {
			allowCalls := 0
			es, received := newTestMqttSource(t, func(string, string) bool { allowCalls++; return true })

			es.onMessage(nil, &fakeMqttMessage{
				topic:   topic,
				payload: []byte(`{"commandToken":"c1","success":true}`),
			})

			assert.Len(t, es.messages, 0, "command traffic must not enter the decode path")
			assert.Equal(t, 0, allowCalls, "command traffic must not spend the tenant's ingest budget")
			assert.Equal(t, 0, *received, "command traffic must not count as an inbound device event")
		})
	}
}

// The exclusion matches the segment after {instanceId}/{tenant} and nothing else,
// so a device is not silently prevented from publishing events on a topic that
// merely contains one of those words further down.
func TestMqttOnMessage_CommandPlaneMatchIsExact(t *testing.T) {
	for _, topic := range []string{
		"inst-1/acme/devices/device-commands/events",
		"inst-1/acme/events/command-responses",
		"inst-1/acme/device-commands-extra",
	} {
		t.Run(topic, func(t *testing.T) {
			es, _ := newTestMqttSource(t, func(string, string) bool { return true })

			es.onMessage(nil, &fakeMqttMessage{topic: topic, payload: []byte(`{"device":"d1"}`)})

			assert.Len(t, es.messages, 1, "only the segment after the tenant selects the command plane")
		})
	}
}

var _ mqtt.Message = (*fakeMqttMessage)(nil)
