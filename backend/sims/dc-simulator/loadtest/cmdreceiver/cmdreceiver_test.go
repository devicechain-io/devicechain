// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDevice registers a device state on the receiver without opening a
// connection, so the pure accounting (recordFrame/Report) is exercisable with no
// broker. The paho wiring (connect/subscribe/respond) is proven live.
func (r *Receiver) newTestDevice(token string) *deviceState {
	ds := &deviceState{
		token:         token,
		commandTopic:  r.commandTopic(token),
		responseTopic: r.responseTopic(),
		ready:         make(chan error, 1),
		distinct:      make(map[string]int),
		subscribed:    true, // a live subscription is assumed for accounting tests
	}
	r.mu.Lock()
	r.devices[token] = ds
	r.mu.Unlock()
	return ds
}

func frame(t *testing.T, token, device, name string) []byte {
	t.Helper()
	raw, err := json.Marshal(deliveryEnvelope{Token: token, DeviceToken: device, Name: name})
	require.NoError(t, err)
	return raw
}

// The topics are the subject↔MQTT map (dots→slashes) the gateway expects: a wrong
// topic means the device subscribes to nothing and every command "drops".
func TestReceiverTopics(t *testing.T) {
	r := New("inst-1", "acme", "tcp://127.0.0.1:1883")
	assert.Equal(t, "inst-1/acme/device-commands/sensor-1", r.commandTopic("sensor-1"))
	assert.Equal(t, "inst-1/acme/command-responses", r.responseTopic())
}

// A well-formed command is recorded once; a REDELIVERY of the same token bumps raw
// but NOT distinct — the at-least-once de-dup that keeps a redelivered command from
// looking like two commands.
func TestRecordFrameDedupsByToken(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883")
	ds := r.newTestDevice("harness-cmd-probe-001")

	tok, ok := r.recordFrame(ds, frame(t, "cmd-A", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)
	assert.Equal(t, "cmd-A", tok)

	// Redelivery of cmd-A, then a genuinely new command cmd-B.
	_, ok = r.recordFrame(ds, frame(t, "cmd-A", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)
	_, ok = r.recordFrame(ds, frame(t, "cmd-B", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)

	assert.Equal(t, 2, r.Distinct("harness-cmd-probe-001"), "two DISTINCT command tokens")

	rep := r.Report()
	dr := rep.Devices["harness-cmd-probe-001"]
	assert.Equal(t, 3, dr.Raw, "three frames received (incl. the redelivery)")
	assert.Equal(t, 2, dr.Distinct, "two distinct commands")
	assert.Equal(t, 3, rep.TotalRaw)
	assert.Equal(t, 2, rep.TotalDistinct)
}

// A frame that does not decode as a command envelope is counted malformed and
// advances NEITHER the raw nor the distinct tally — miscounting it as a command
// would corrupt the redelivery measurement.
func TestRecordFrameMalformed(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883")
	ds := r.newTestDevice("harness-cmd-probe-001")

	tok, ok := r.recordFrame(ds, []byte("{not json"))
	assert.False(t, ok)
	assert.Empty(t, tok)

	rep := r.Report()
	dr := rep.Devices["harness-cmd-probe-001"]
	assert.Equal(t, 0, dr.Raw)
	assert.Equal(t, 0, dr.Distinct)
	assert.Equal(t, 1, dr.Malformed)
}

// A device that never got a confirmed SUBACK is reported un-subscribed and listed
// Blind — its silence is never read as a clean "no command arrived" (the L2c
// fail-closed-about-own-blindness discipline). Distinct on an unknown device is 0.
func TestReceiverBlindDeviceSurfaced(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883")
	// A device that connected but never acked its subscription: subscribed stays false.
	ds := r.newTestDevice("harness-cmd-probe-001")
	ds.subscribed = false

	rep := r.Report()
	assert.Equal(t, []string{"harness-cmd-probe-001"}, rep.Blind)
	assert.False(t, rep.Devices["harness-cmd-probe-001"].Subscribed)
	assert.Equal(t, 0, r.Distinct("unknown-device"), "an unknown device has received nothing")
}
