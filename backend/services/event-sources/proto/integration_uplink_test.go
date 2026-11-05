// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
)

func uplinkPayload() *model.UnresolvedIntegrationUplinkPayload {
	port, cnt := int32(2), uint32(4294967295)
	return &model.UnresolvedIntegrationUplinkPayload{
		Frame: []byte{0x01, 0x00, 0xd7}, FPort: &port, FCnt: &cnt,
		Json: []byte(`{"a":1}`), Decoded: []byte(`{"t":21.5}`),
		Link:        map[string]float64{"rssi": -36, "snr": 10.5, "gateways": 2},
		MaxReadings: 11, ProfileVersionToken: "v3",
	}
}

func TestAnIntegrationUplinkRoundTripsThroughTheEvent(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source: "integration:cs", Device: "sensor-001", EventType: eventtype.IntegrationUplink,
		OccurredTime: time.Now().UTC().Truncate(time.Second), ProcessedTime: time.Now().UTC().Truncate(time.Second),
		Integration: "cs", SealedIntegrationToken: []byte("sealed"), IntegrationIdentifier: "70B3D57ED0000001",
		Payload: uplinkPayload(),
	}
	b, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	require.NoError(t, err)
	got, err := UnmarshalUnresolvedEvent(b)
	require.NoError(t, err)
	require.Equal(t, eventtype.IntegrationUplink, got.EventType)
	require.Equal(t, uplinkPayload(), got.Payload, "every field, the 32-bit frame counter at its maximum included")
	require.Equal(t, "cs", got.Integration)
}

// An absent port, counter and body stays absent, and a present-but-zero port is not mistaken for
// absent: fPort 0 is the LoRaWAN MAC port and a decoder branches on it.
func TestAnIntegrationUplinkKeepsAbsentApartFromZero(t *testing.T) {
	b, err := MarshalPayloadForIntegrationUplinkEvent(&model.UnresolvedIntegrationUplinkPayload{})
	require.NoError(t, err)
	got, err := UnmarshalPayloadForIntegrationUplinkEvent(b)
	require.NoError(t, err)
	require.Nil(t, got.FPort)
	require.Nil(t, got.FCnt)
	require.Nil(t, got.Frame)
	require.Nil(t, got.Json)

	zero, zcnt := int32(0), uint32(0)
	b, err = MarshalPayloadForIntegrationUplinkEvent(&model.UnresolvedIntegrationUplinkPayload{FPort: &zero, FCnt: &zcnt})
	require.NoError(t, err)
	got, err = UnmarshalPayloadForIntegrationUplinkEvent(b)
	require.NoError(t, err)
	require.NotNil(t, got.FPort)
	require.Equal(t, int32(0), *got.FPort)
	require.NotNil(t, got.FCnt)
}

// The same payload always encodes to the same bytes, whatever order the link map is walked in.
func TestAnIntegrationUplinkEncodesDeterministically(t *testing.T) {
	first, err := MarshalPayloadForIntegrationUplinkEvent(uplinkPayload())
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		again, err := MarshalPayloadForIntegrationUplinkEvent(uplinkPayload())
		require.NoError(t, err)
		require.Equal(t, first, again)
	}
}

// A payload of the wrong Go type is refused, and its text never echoes the payload (it can hold a
// device's frame).
func TestAnIntegrationUplinkEventRefusesAForeignPayloadWithoutEchoingIt(t *testing.T) {
	_, err := MarshalUnresolvedPayload(eventtype.IntegrationUplink,
		&model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{{Measurements: map[string]string{"secret": "9"}}}},
		eventtime.WireCompact)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}
