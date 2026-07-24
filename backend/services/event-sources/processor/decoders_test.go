// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/require"
)

// TestJsonDecoderRejectsDeviceStateChange is a SECURITY pin (ADR-067): StateChange
// (presence) is a platform-producer event a presence-asserting adapter emits directly
// over the proto wire contract, never through this device-facing JSON decoder. If the
// decoder accepted it, any device credential could forge its own presence — assert
// itself permanently CONNECTED with an unbeatable session id that the projection can
// never supersede (the sweep skips ASSERTED and no data event flips it), hiding the
// device's own death. The decoder MUST reject it. Mutation control: re-adding a
// BuildStateChangePayload case to Decode turns this red.
func TestJsonDecoderRejectsDeviceStateChange(t *testing.T) {
	jd := NewJsonDecoder(map[string]string{})
	payload := []byte(`{"device":"d1","eventType":"` + model.StateChange.String() +
		`","payload":{"state":"CONNECTED","sessionId":"18446744073709551615"}}`)

	_, _, err := jd.Decode(payload)
	require.Error(t, err, "a device must not be able to forge a presence StateChange through ingest")
	require.True(t, strings.Contains(err.Error(), "platform-produced"),
		"the rejection must be the deliberate platform-producer guard, not an incidental parse error: %v", err)
}

// TestJsonDecoderCannotSetAuthenticatedTransport is THE load-bearing security pin for
// the transport-auth marker: a device controls the raw JSON bytes on the device->
// inbound-events path, so if this decoder ever gained a field for AuthenticatedTransport
// a device could forge the marker and bypass credential auth under deviceAuthMode=required.
// JsonEvent has no such field, so an injected "authenticatedTransport":true is ignored and
// the assembled event stays false. Mutation control: adding an authenticatedTransport
// field to JsonEvent/AssembleEvent turns this red.
func TestJsonDecoderCannotSetAuthenticatedTransport(t *testing.T) {
	jd := NewJsonDecoder(map[string]string{})
	// A well-formed device Measurement event that ALSO tries to inject the marker.
	payload := []byte(`{"device":"sensor-001","eventType":"Measurement","authenticatedTransport":true,"payload":{"measurements":{"temp":21.5}}}`)

	event, _, err := jd.Decode(payload)
	require.NoError(t, err)
	require.NotNil(t, event)
	require.False(t, event.AuthenticatedTransport,
		"a device must NOT be able to set AuthenticatedTransport through the ingest decoder — the whole marker trust rests on this")
}
