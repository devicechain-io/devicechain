// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
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
	// Everything else in the document is valid with the setting on: lift the refusal (which
	// only this package's tests can) and the rest validates.
	shipMqttFailClosedForTest(t)
	if err := c.Validate(); err != nil {
		t.Fatalf("with the refusal lifted, a document with the setting on must validate: %v", err)
	}
}

// shipMqttFailClosedForTest lifts the refusal for one test, as the build that ships the
// back-off will for good.
func shipMqttFailClosedForTest(t *testing.T) {
	t.Helper()
	saved := mqttFailClosedShipped
	mqttFailClosedShipped = true
	t.Cleanup(func() { mqttFailClosedShipped = saved })
}

// 🔴 UNTIL ITS PREREQUISITES SHIP, THE SETTING IS REFUSED. The reader over the message store,
// the back-off and the presence debounce have shipped, but the deployed broker is not the
// version the gateway behaviour is pinned against, and the cutover and the overload
// measurement are not done. A document that sets it must fail validation, saying what is
// missing, and the same document with it off must not.
//
// GUARD: kills a Validate that accepts the setting (the outage switch is live), and one that
// refuses the document for any other reason (the control passes with only the key changed).
func TestMqttFailClosedIsRefusedUntilTheBackOffShips(t *testing.T) {
	c := NewDefaultInstanceConfiguration()
	c.Infrastructure.Nats.MqttFailClosed = true
	c.ApplyDefaults()
	err := c.Validate()
	if !errors.Is(err, ErrMqttFailClosedNotAvailable) {
		t.Fatalf("Validate with mqttFailClosed on = %v, want ErrMqttFailClosedNotAvailable", err)
	}
	for _, want := range []string{"infrastructure.nats.mqttFailClosed", "not available in this build",
		"nats-server 2.14.4", "verified only against 2.15.0", "capture stream", "measurement under overload"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
	c.Infrastructure.Nats.MqttFailClosed = false
	if err := c.Validate(); err != nil {
		t.Fatalf("control: the same document with the setting off must validate: %v", err)
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

// 🔑 L5: THE DISK BUDGET WITH THE SETTING ON. Turning it on moves the message store from the
// safety cap to the hot ceiling while the device-events capture stream keeps its own (both
// exist until the capture path is retired), so the default install reserves more. It must
// still fit the default volume with the same headroom floor as with it off: the unbounded
// gateway stores and the consumers' state live in what is left.
//
// GUARD: kills a reservation that does not count the store's larger bound with the setting on
// (TestStreamReservationFitsBudget would then keep passing on a volume a fresh install with it
// on overruns), and a default ceiling that no longer fits once the store takes it.
func TestWithMqttFailClosedOnTheDefaultReservationStillFitsTheVolume(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := NewDefaultInstanceConfiguration()
		c.Infrastructure.Nats.MqttFailClosed = on
		c.ApplyDefaults()
		n := c.Infrastructure.Nats
		var reserved int64
		for _, s := range streams.Suffixes() {
			reserved += n.StreamMaxBytesFor(s)
		}
		reserved += n.MqttStoreReservation()
		reserved += n.KvReservation()
		if on {
			off := NewDefaultInstanceConfiguration()
			off.ApplyDefaults()
			delta := n.StreamMaxBytesFor(streams.DeviceEventsCapture) - off.Infrastructure.Nats.MqttStoreMaxBytes
			if got := n.MqttStoreReservation() - off.Infrastructure.Nats.MqttStoreReservation(); got != delta {
				t.Errorf("with the setting on the reservation moves by %d B, want the store's move from the safety cap "+
					"to the capture ceiling, %d B", got, delta)
			}
		}
		if reserved > maxFileStore {
			t.Errorf("mqttFailClosed=%v: the default reservation %d B exceeds max_file_store %d B", on, reserved, maxFileStore)
		}
		const headroomFloor = 512 << 20
		if got := maxFileStore - reserved; got < headroomFloor {
			t.Errorf("mqttFailClosed=%v: only %d B left unreserved, want at least %d B", on, got, int64(headroomFloor))
		}
	}
}
