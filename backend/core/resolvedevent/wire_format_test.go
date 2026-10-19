// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 🔴 THE ROLLING-UPGRADE GUARANTEE, for the resolved stream: event-management, device-state
// and event-processing pods still on the release before the compact format read only what
// that release wrote, and DROP anything else. So the LEGACY format this release writes by
// default must be that, byte for byte. These bytes were captured by running
// MarshalResolvedEvent on origin/main 9b680acf6 over goldenResolvedEvents.
var mainResolvedHex = map[string]string{
	"alert out of range": "0a04687474701a03642d313a1e313530302d30312d30325430333a30343a30352e3030303030303030365a4223393939392d31322d33315432333a35393a35392e3939393939393939392d30353a3030480352730a2b0a054641554c5410021a0570726f626522036465762a14323330302d30312d30315430303a30303a30305a0a1d0a0545504f43482a14313937302d30312d30315430303a30303a30305a0a250a035052452a1e313936392d31322d33315432333a35393a35392e3939393939393939395a",
	"location":           "0a046d7174741a0a73656e736f722d3030313a23323032362d31302d31365430343a33303a31352e3132333435363738392d30353a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a480152330a310a0434352e3512052d37332e36221e323032362d31302d31365430393a33303a31342e3030303030303030315a3a0239305a07747261636b6572620574726b40317804",
	"measurement full":   "0a04687474701205616c742d311a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f1805220d0a04617265611203612d3118093a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a480252780a480a1b0a0474656d70120432312e351801220343656c2a06444f55424c450a090a0368756d12023430121e323032362d31302d31365430393a33303a31342e3938373635343332315a0a2c0a0a0a0474656d7012023232121e323032362d31302d31365430393a33303a31342e3030303030303030315a5a0b64657669636570756c7365620770756c736540336a070a03672d31100272056578742d317807800101",
	"presence disabled":  "0a046d7174741a056465762d313a14313937302d30312d30315430303a30303a30305a4214303030312d30312d30315430303a30303a30305a4804522d0a0c444953434f4e4e45435445441205636c6f736518072214323032362d31302d31365430393a33303a31355a6203704031800101",
}

// goldenResolvedEvents is, field for field, the input the origin/main capture ran on.
func goldenResolvedEvents() map[string]*ResolvedEvent {
	plus2 := time.FixedZone("", 2*3600)
	minus5 := time.FixedZone("", -5*3600)
	occ := time.Date(2026, 10, 16, 11, 30, 15, 123456789, plus2)
	proc := time.Date(2026, 10, 16, 9, 30, 15, 125000007, time.UTC)
	e1 := time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)
	e2 := time.Date(2026, 10, 16, 4, 30, 14, 1, minus5)
	return map[string]*ResolvedEvent{
		"measurement full": {Source: "http", AltId: strp("alt-1"), SourceDeviceToken: "devicepulse-00001",
			Anchors:         []ResolvedAnchor{{AnchorType: "customer", AnchorToken: "acme-co", RelationshipId: 5}, {AnchorType: "area", AnchorToken: "a-1", RelationshipId: 9}},
			DeviceTypeToken: "devicepulse", ProfileVersionToken: "pulse@3", ExternalId: "ext-1",
			ScopeMemberships: []GroupRef{{GroupToken: "g-1", Version: 2}},
			FenceSetVersion:  7, SourceDeviceDisabled: true,
			OccurredTime: occ, ProcessedTime: proc, EventType: eventtype.Measurement,
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
				{Entries: []ResolvedMeasurementEntry{{Name: "temp", Value: "21.5", Classifier: u64p(1), Unit: strp("Cel"), DataType: strp("DOUBLE")}, {Name: "hum", Value: "40"}}, OccurredTime: e1},
				{Entries: []ResolvedMeasurementEntry{{Name: "temp", Value: "22"}}, OccurredTime: e2},
			}}},
		"location": {Source: "mqtt", SourceDeviceToken: "sensor-001", DeviceTypeToken: "tracker",
			ProfileVersionToken: "trk@1", FenceSetVersion: 4,
			Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			OccurredTime: occ.In(minus5), ProcessedTime: proc, EventType: eventtype.Location,
			Payload: &ResolvedLocationsPayload{Entries: []ResolvedLocationEntry{
				{Latitude: strp("45.5"), Longitude: strp("-73.6"), Heading: strp("90"), OccurredTime: e2}}}},
		"alert out of range": {Source: "http", SourceDeviceToken: "d-1",
			Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			OccurredTime: time.Date(1500, 1, 2, 3, 4, 5, 6, time.UTC), ProcessedTime: time.Date(9999, 12, 31, 23, 59, 59, 999999999, minus5),
			EventType: eventtype.Alert,
			Payload: &ResolvedAlertsPayload{Entries: []ResolvedAlertEntry{
				{Type: "FAULT", Level: 2, Message: "probe", Source: "dev", OccurredTime: time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)},
				{Type: "EPOCH", OccurredTime: time.Unix(0, 0)},
				{Type: "PRE", OccurredTime: time.Unix(-1, 999999999)}}}},
		"presence disabled": {Source: "mqtt", SourceDeviceToken: "dev-1", ProfileVersionToken: "p@1",
			OccurredTime: time.Unix(0, 0).UTC(), ProcessedTime: time.Time{}, EventType: eventtype.StateChange,
			SourceDeviceDisabled: true,
			Payload:              &ResolvedStateChangePayload{State: "DISCONNECTED", Reason: "close", SessionId: 7, OccurredTime: strp("2026-10-16T09:30:15Z")}},
	}
}

func TestTheLegacyResolvedFormatIsByteForByteWhatTheReleaseBeforeWrote(t *testing.T) {
	events := goldenResolvedEvents()
	require.Len(t, mainResolvedHex, len(events))
	for name, event := range events {
		got, err := MarshalResolvedEventAs(event, eventtime.WireLegacy)
		require.NoError(t, err, name)
		require.Equal(t, mainResolvedHex[name], hex.EncodeToString(got), "%s: legacy bytes differ from origin/main's", name)

		// The process default is the compact format.
		def, err := MarshalResolvedEvent(event)
		require.NoError(t, err, name)
		compact, err := MarshalResolvedEventAs(event, eventtime.WireCompact)
		require.NoError(t, err, name)
		require.Equal(t, compact, def, "%s: the default format is not compact", name)

		// And nothing in it is a field the release before does not know.
		var pb PResolvedEvent
		require.NoError(t, proto.Unmarshal(got, &pb), name)
		require.Nil(t, pb.Occurred, name)
		require.Nil(t, pb.Processed, name)
	}
}

// Both formats decode, through this release's reader, to the same event; the release before
// reads only the legacy one, whose envelope strings it parses.
func TestBothResolvedFormatsDecodeToTheSameEvent(t *testing.T) {
	for name, event := range goldenResolvedEvents() {
		legacy, err := MarshalResolvedEventAs(event, eventtime.WireLegacy)
		require.NoError(t, err, name)
		compact, err := MarshalResolvedEventAs(event, eventtime.WireCompact)
		require.NoError(t, err, name)
		fromLegacy, err := UnmarshalResolvedEvent(legacy)
		require.NoError(t, err, name)
		fromCompact, err := UnmarshalResolvedEvent(compact)
		require.NoError(t, err, name)
		require.Equal(t, fromLegacy, fromCompact, name)
		require.True(t, fromCompact.OccurredTime.Equal(event.OccurredTime), name)
		require.True(t, fromCompact.ProcessedTime.Equal(event.ProcessedTime), name)

		var pb PResolvedEvent
		require.NoError(t, proto.Unmarshal(legacy, &pb), name)
		_, err = time.Parse(time.RFC3339Nano, pb.OccurredTime)
		require.NoError(t, err, "%s: the release before parses the legacy envelope", name)
	}
}

// A format that is neither is refused before anything is written.
func TestAnUnknownResolvedWireFormatIsRefused(t *testing.T) {
	event := goldenResolvedEvents()["measurement full"]
	for _, format := range []eventtime.WireFormat{0, 3} {
		_, err := MarshalResolvedEventAs(event, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat)
		_, err = MarshalPayloadForMeasurementsEvent(event.Payload.(*ResolvedMeasurementsPayload), format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat)
		_, err = MarshalPayloadForLocationsEvent(&ResolvedLocationsPayload{}, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat)
		_, err = MarshalPayloadForAlertsEvent(&ResolvedAlertsPayload{}, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat)
	}
}
