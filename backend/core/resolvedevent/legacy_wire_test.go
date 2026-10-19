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

// The resolved-events stream is RETAINED, so a consumer of a release that writes each instant
// as a count still reads messages written before it, in which every instant is an RFC 3339
// string: across an upgrade, and for as long afterwards as the replay preview and detection
// replay read history back. These are such messages, byte for byte: written by
// MarshalResolvedEvent as it stood before the count existed (origin/main 9b680acf6), for the
// events legacyResolvedEvents describes. Their envelopes carry the device's +02:00 offset,
// which the string form kept and the count does not.
var legacyResolvedHex = map[string]string{
	"measurement": "0a04687474701a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f18053a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4802523f0a3d0a1b0a0474656d70120432312e351801220343656c2a06444f55424c45121e323032362d31302d31365430393a33303a31342e3938373635343332315a5a0b64657669636570756c7365620770756c7365403372156578742d64657669636570756c73652d3030303031",
	"location":    "0a046d7174741a0a73656e736f722d3030313a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4801522f0a2d0a0434352e3512052d37332e36221e323032362d31302d31365430393a33303a31342e3938373635343332315a5a07747261636b6572620574726b40317804",
	"alert":       "0a04687474701a1164657669636570756c73652d30303030313a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4803523e0a3c0a0c53454e534f525f4641554c5410021a0570726f626522036465762a1e323032362d31302d31365430393a33303a31342e3938373635343332315a",
}

// legacyResolvedEvents is what each legacy message says, every instant in UTC as a reader
// now returns it: the same instants, to the nanosecond, as the offsets they were written with.
func legacyResolvedEvents() map[string]*ResolvedEvent {
	occurred := time.Date(2026, 10, 16, 9, 30, 15, 123456789, time.UTC)
	processed := time.Date(2026, 10, 16, 9, 30, 15, 125000007, time.UTC)
	entry := time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)
	return map[string]*ResolvedEvent{
		"measurement": {Source: "http", SourceDeviceToken: "devicepulse-00001",
			Anchors:         []ResolvedAnchor{{AnchorType: "customer", AnchorToken: "acme-co", RelationshipId: 5}},
			DeviceTypeToken: "devicepulse", ProfileVersionToken: "pulse@3", ExternalId: "ext-devicepulse-00001",
			ScopeMemberships: []GroupRef{},
			OccurredTime:     occurred, ProcessedTime: processed, EventType: eventtype.Measurement,
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
				Entries:      []ResolvedMeasurementEntry{{Name: "temp", Value: "21.5", Classifier: u64p(1), Unit: strp("Cel"), DataType: strp("DOUBLE")}},
				OccurredTime: entry}}}},
		"location": {Source: "mqtt", SourceDeviceToken: "sensor-001", DeviceTypeToken: "tracker",
			ProfileVersionToken: "trk@1", FenceSetVersion: 4,
			Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			OccurredTime: occurred, ProcessedTime: processed, EventType: eventtype.Location,
			Payload: &ResolvedLocationsPayload{Entries: []ResolvedLocationEntry{
				{Latitude: strp("45.5"), Longitude: strp("-73.6"), OccurredTime: entry}}}},
		"alert": {Source: "http", SourceDeviceToken: "devicepulse-00001",
			Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			OccurredTime: occurred, ProcessedTime: processed, EventType: eventtype.Alert,
			Payload: &ResolvedAlertsPayload{Entries: []ResolvedAlertEntry{
				{Type: "SENSOR_FAULT", Level: 2, Message: "probe", Source: "dev", OccurredTime: entry}}}},
	}
}

// currentResolvedSize is each legacy message's size written in the current format. The
// strings were 35 (the offset envelope), 30 (the UTC one) and 30 (the entry) bytes plus a tag
// and a length; the counts are eight bytes plus a tag, two bytes of it on the envelope.
var currentResolvedSize = map[string]int{
	"measurement": 229 - 37 - 32 - 32 + 10 + 10 + 9, // 157
	"location":    156 - 37 - 32 - 32 + 10 + 10 + 9, // 84
	"alert":       160 - 37 - 32 - 32 + 10 + 10 + 9, // 88
}

// A message written before the count existed decodes to the event it was written for: every
// field, every instant to the nanosecond.
func TestALegacyResolvedMessageDecodesToItsEvent(t *testing.T) {
	want := legacyResolvedEvents()
	for name, h := range legacyResolvedHex {
		legacy, err := hex.DecodeString(h)
		require.NoError(t, err, name)
		got, err := UnmarshalResolvedEvent(legacy)
		require.NoError(t, err, name)
		require.Equal(t, want[name], got, name)
		require.Equal(t, time.UTC, got.OccurredTime.Location(), name)
	}
}

// The same event written now is smaller by what the counts save, carries no string instant,
// and decodes to exactly what the legacy message decodes to: a consumer cannot tell which
// side of the upgrade wrote it.
func TestTheCurrentFormatCarriesTheLegacyEventInFewerBytes(t *testing.T) {
	for name, h := range legacyResolvedHex {
		legacy, err := hex.DecodeString(h)
		require.NoError(t, err, name)
		fromLegacy, err := UnmarshalResolvedEvent(legacy)
		require.NoError(t, err, name)

		current, err := MarshalResolvedEventAs(fromLegacy, eventtime.WireCompact)
		require.NoError(t, err, name)
		require.Equal(t, currentResolvedSize[name], len(current), "%s: written in %d bytes (legacy %d)", name, len(current), len(legacy))

		var pb PResolvedEvent
		require.NoError(t, proto.Unmarshal(current, &pb), name)
		require.Empty(t, pb.OccurredTime, name)
		require.Empty(t, pb.ProcessedTime, name)
		require.NotNil(t, pb.Occurred, name)
		require.NotNil(t, pb.Processed, name)

		fromCurrent, err := UnmarshalResolvedEvent(current)
		require.NoError(t, err, name)
		require.Equal(t, fromLegacy, fromCurrent, name)
	}
}

// A message carrying an envelope instant in neither form is refused, as one with an
// unparseable string always was: an absent instant is never read as some default.
func TestAResolvedMessageWithNoInstantIsRefused(t *testing.T) {
	nanos := int64(1)
	for name, pb := range map[string]*PResolvedEvent{
		"no occurred":  {Source: "s", SourceDeviceToken: "d", Processed: &nanos},
		"no processed": {Source: "s", SourceDeviceToken: "d", Occurred: &nanos},
	} {
		pb.EventType = int64(eventtype.NewRelationship)
		raw, err := proto.Marshal(pb)
		require.NoError(t, err, name)
		_, err = UnmarshalResolvedEvent(raw)
		require.Error(t, err, name)
	}
}

// Every instant survives the current format to the nanosecond, including ones a
// microsecond-precision store would truncate and ones before the epoch; and an instant past
// what the count holds survives in the string form.
func TestResolvedInstantsRoundTripToTheNanosecond(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 10, 16, 9, 30, 15, 1, time.UTC),
		time.Date(2026, 10, 16, 9, 30, 15, 999999999, time.FixedZone("y", -5*3600)),
		time.Date(1969, 7, 20, 20, 17, 40, 123456789, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999990, time.UTC),
		{},
	} {
		ev := &ResolvedEvent{Source: "s", SourceDeviceToken: "d", OccurredTime: at, ProcessedTime: at.Add(7),
			EventType: eventtype.Measurement,
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
				{OccurredTime: at.Add(-3), Entries: []ResolvedMeasurementEntry{{Name: "a", Value: "1"}}}}}}
		encoded, err := MarshalResolvedEventAs(ev, eventtime.WireCompact)
		require.NoError(t, err)
		got, err := UnmarshalResolvedEvent(encoded)
		require.NoError(t, err)
		require.True(t, got.OccurredTime.Equal(at), "%s came back as %s", at, got.OccurredTime)
		require.True(t, got.ProcessedTime.Equal(at.Add(7)), "%s", at)
		entry := got.Payload.(*ResolvedMeasurementsPayload).Entries[0].OccurredTime
		require.True(t, entry.Equal(at.Add(-3)), "%s: entry came back as %s", at, entry)
	}
}
