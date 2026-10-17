// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

// The fail-closed MQTT acknowledgement is OFF unless the document says otherwise, and while
// it is off the message store's ceiling and the disk reservation are exactly what they were
// before the setting existed.
func TestMqttFailClosedIsOffByDefaultAndChangesNothingWhileOff(t *testing.T) {
	c := NewDefaultInstanceConfiguration()
	c.ApplyDefaults()
	n := c.Infrastructure.Nats
	if n.MqttFailClosed {
		t.Fatal("mqttFailClosed must default to off")
	}
	if got := n.MqttMessageStoreMaxBytes(); got != DefaultMqttStoreMaxBytes {
		t.Errorf("with the setting off the message store is bounded at %d B, want mqttStoreMaxBytes (%d B)",
			got, DefaultMqttStoreMaxBytes)
	}
	if got, want := n.MqttStoreReservation(), DefaultMqttStoreMaxBytes+2*DefaultMqttQoS2StoreMaxBytes; got != want {
		t.Errorf("with the setting off the MQTT reservation is %d B, want %d B", got, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the default document must validate: %v", err)
	}
}

// With it on, the message store is the platform's buffer for QoS 1 device publishes and takes
// the capture stream's hot ceiling, and the reservation counts that ceiling rather than the
// safety cap: the store reserves what it is bounded at.
func TestMqttFailClosedGivesTheMessageStoreTheCaptureCeilingAndBudgetsIt(t *testing.T) {
	c := NewDefaultInstanceConfiguration()
	c.Infrastructure.Nats.MqttFailClosed = true
	c.Infrastructure.Nats.StreamMaxBytes = 3 << 30
	c.ApplyDefaults()
	n := c.Infrastructure.Nats
	want := n.StreamMaxBytesFor(streams.DeviceEventsCapture)
	if want != 3<<30 {
		t.Fatalf("premise: the capture stream's ceiling is %d B, want the raised hot ceiling", want)
	}
	if got := n.MqttMessageStoreMaxBytes(); got != want {
		t.Errorf("the fail-closed message store is bounded at %d B, want the capture ceiling %d B", got, want)
	}
	if got := n.MqttStoreReservation(); got != want+2*n.MqttQoS2StoreMaxBytes {
		t.Errorf("the reservation is %d B, want the message store's %d B plus both QoS 2 stores", got, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a document with the setting on must validate: %v", err)
	}
}

// The key decodes under the strict loader's rules (unknown keys refused), so an operator who
// writes it gets it, and a misspelling of it is still refused.
func TestMqttFailClosedKeyDecodesStrictly(t *testing.T) {
	decode := func(doc string) (*InstanceConfiguration, error) {
		dec := json.NewDecoder(bytes.NewReader([]byte(doc)))
		dec.DisallowUnknownFields()
		c := &InstanceConfiguration{}
		return c, dec.Decode(c)
	}
	c, err := decode(`{"infrastructure":{"nats":{"mqttFailClosed":true}}}`)
	if err != nil {
		t.Fatalf("decoding mqttFailClosed: %v", err)
	}
	if !c.Infrastructure.Nats.MqttFailClosed {
		t.Error("mqttFailClosed: true decoded as off")
	}
	if _, err := decode(`{"infrastructure":{"nats":{"mqttFailClosd":true}}}`); err == nil {
		t.Error("a misspelt key must be refused, not read as the setting being off")
	}
}
