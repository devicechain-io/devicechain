// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The existing resolved-event round trip carries a location payload whose Entries
// slice is EMPTY, which is precisely how the location blind spot survived: nothing
// in the repository ever asserted that a coordinate came back. These tests are the
// counterweight — they push real, DISTINCT values through
// MarshalPayloadForLocationsEvent/UnmarshalPayloadForLocationsEvent and read each
// field back individually. Distinct literals are load-bearing: identical values
// would let a marshal that crossed Accuracy with Speed pass.

// s is a local helper so each literal below reads as its own distinct value.
func s(v string) *string {
	return &v
}

// at parses a fixture instant. A resolved entry always carries one — see
// model.ResolvedMeasurementsEntry.OccurredTime — so these are values, not pointers.
func at(t *testing.T, v string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, v)
	require.NoError(t, err, "fixture time %q", v)
	return parsed
}

// Every field of a fix must survive the protobuf round trip. A dropped field is
// silent: the payload still decodes, the entry count is still right, and the fix
// simply arrives at event-management and device-state without its speed or heading.
func TestLocationPayloadRoundTripCarriesEveryField(t *testing.T) {
	occurred := at(t, "2026-08-09T14:32:07.125Z")
	payload := &model.ResolvedLocationsPayload{
		Entries: []model.ResolvedLocationEntry{{
			Latitude:     s("33.749"),
			Longitude:    s("-84.388"),
			Elevation:    s("320.5"),
			Accuracy:     s("4.2"),
			Speed:        s("1.75"),
			Heading:      s("271.5"),
			OccurredTime: occurred,
		}},
	}

	encoded, err := MarshalPayloadForLocationsEvent(payload)
	require.NoError(t, err)

	decoded, err := UnmarshalPayloadForLocationsEvent(encoded, time.Time{})
	require.NoError(t, err)
	require.Len(t, decoded.Entries, 1)

	got := decoded.Entries[0]
	require.NotNil(t, got.Latitude, "latitude did not survive the round trip")
	assert.Equal(t, "33.749", *got.Latitude)
	require.NotNil(t, got.Longitude, "longitude did not survive the round trip")
	assert.Equal(t, "-84.388", *got.Longitude)
	require.NotNil(t, got.Elevation, "elevation did not survive the round trip")
	assert.Equal(t, "320.5", *got.Elevation)
	require.NotNil(t, got.Accuracy, "accuracy did not survive the round trip")
	assert.Equal(t, "4.2", *got.Accuracy)
	require.NotNil(t, got.Speed, "speed did not survive the round trip")
	assert.Equal(t, "1.75", *got.Speed)
	require.NotNil(t, got.Heading, "heading did not survive the round trip")
	assert.Equal(t, "271.5", *got.Heading)
	assert.True(t, got.OccurredTime.Equal(occurred),
		"the sample's own instant did not survive the round trip: got %v want %v", got.OccurredTime, occurred)
}

// The proto fields are `optional string`, so they carry explicit presence: absent and
// present-but-empty are genuinely different on the wire, and both must be preserved.
// A device that reports no speed is not a device reporting a speed of "".
func TestLocationPayloadRoundTripPreservesNilAndEmpty(t *testing.T) {
	payload := &model.ResolvedLocationsPayload{
		Entries: []model.ResolvedLocationEntry{{
			Latitude:  s("41.8781"),
			Longitude: s("-87.6298"),
			// Elevation and Accuracy deliberately absent.
			Speed:        s(""), // present, empty — must NOT collapse to absent.
			Heading:      s("0"),
			OccurredTime: at(t, "2026-08-09T14:32:07.125Z"),
		}},
	}

	encoded, err := MarshalPayloadForLocationsEvent(payload)
	require.NoError(t, err)

	decoded, err := UnmarshalPayloadForLocationsEvent(encoded, time.Time{})
	require.NoError(t, err)
	require.Len(t, decoded.Entries, 1)

	got := decoded.Entries[0]
	require.NotNil(t, got.Latitude)
	assert.Equal(t, "41.8781", *got.Latitude)
	require.NotNil(t, got.Longitude)
	assert.Equal(t, "-87.6298", *got.Longitude)
	assert.Nil(t, got.Elevation, "an absent elevation must come back absent, not as an empty string")
	assert.Nil(t, got.Accuracy, "an absent accuracy must come back absent, not as an empty string")
	require.NotNil(t, got.Speed, "a present empty speed must come back present, not absent")
	assert.Equal(t, "", *got.Speed)
	require.NotNil(t, got.Heading, "heading \"0\" is a real bearing (due north) and must not be dropped")
	assert.Equal(t, "0", *got.Heading)
}

// A batched fix set must round-trip entry-for-entry, in order, each keeping its own
// values — the guard against a marshal loop that reuses one entry pointer, which
// keeps the count right while collapsing a track into one repeated point.
func TestLocationPayloadRoundTripKeepsPerEntryValuesAndOrder(t *testing.T) {
	firstTime := at(t, "2026-08-09T14:32:07.125Z")
	secondTime := at(t, "2026-08-09T14:32:09.500Z")
	payload := &model.ResolvedLocationsPayload{
		Entries: []model.ResolvedLocationEntry{
			{
				Latitude: s("33.749"), Longitude: s("-84.388"), Elevation: s("320.5"),
				Accuracy: s("4.2"), Speed: s("1.75"), Heading: s("271.5"),
				OccurredTime: firstTime,
			},
			{
				Latitude: s("47.6062"), Longitude: s("-122.3321"), Elevation: s("54.25"),
				Accuracy: s("9.8"), Speed: s("12.5"), Heading: s("18.75"),
				OccurredTime: secondTime,
			},
		},
	}

	encoded, err := MarshalPayloadForLocationsEvent(payload)
	require.NoError(t, err)

	decoded, err := UnmarshalPayloadForLocationsEvent(encoded, time.Time{})
	require.NoError(t, err)
	require.Len(t, decoded.Entries, 2)

	assertLocationEntry(t, decoded.Entries[0], "33.749", "-84.388", "320.5", "4.2", "1.75", "271.5", firstTime)
	assertLocationEntry(t, decoded.Entries[1], "47.6062", "-122.3321", "54.25", "9.8", "12.5", "18.75", secondTime)
}

// The generic dispatchers are a SEPARATE place a location field can be dropped: they
// pick the concrete (un)marshaller by event type, and this is the path every resolved
// event actually travels. Values must survive it too, not just the direct call.
func TestLocationRoundTripsThroughGenericDispatchers(t *testing.T) {
	occurred := at(t, "2026-08-09T14:32:07.125Z")
	payload := &model.ResolvedLocationsPayload{
		Entries: []model.ResolvedLocationEntry{{
			Latitude:     s("33.749"),
			Longitude:    s("-84.388"),
			Elevation:    s("320.5"),
			Accuracy:     s("4.2"),
			Speed:        s("1.75"),
			Heading:      s("271.5"),
			OccurredTime: occurred,
		}},
	}

	encoded, err := MarshalResolvedPayload(esmodel.Location, payload)
	require.NoError(t, err)

	generic, err := UnmarshalResolvedPayload(esmodel.Location, encoded, time.Time{})
	require.NoError(t, err)

	decoded, ok := generic.(*model.ResolvedLocationsPayload)
	require.True(t, ok, "expected *model.ResolvedLocationsPayload, got %T", generic)
	require.Len(t, decoded.Entries, 1)
	assertLocationEntry(t, decoded.Entries[0], "33.749", "-84.388", "320.5", "4.2", "1.75", "271.5", occurred)
}

// A full resolved event carrying a NON-EMPTY location payload must arrive with its
// values intact — the end-to-end shape the existing empty-Entries round trip could
// never have caught.
func TestResolvedEventCarriesLocationValues(t *testing.T) {
	occurred := at(t, "2026-08-09T14:32:07.125Z")
	event := &model.ResolvedEvent{
		Source:            "http1",
		SourceDeviceToken: "device-001",
		EventType:         esmodel.Location,
		Payload: &model.ResolvedLocationsPayload{
			Entries: []model.ResolvedLocationEntry{{
				Latitude:     s("33.749"),
				Longitude:    s("-84.388"),
				Elevation:    s("320.5"),
				Accuracy:     s("4.2"),
				Speed:        s("1.75"),
				Heading:      s("271.5"),
				OccurredTime: occurred,
			}},
		},
	}

	encoded, err := MarshalResolvedEvent(event)
	require.NoError(t, err)

	decoded, err := UnmarshalResolvedEvent(encoded)
	require.NoError(t, err)

	payload, ok := decoded.Payload.(*model.ResolvedLocationsPayload)
	require.True(t, ok, "expected *model.ResolvedLocationsPayload, got %T", decoded.Payload)
	require.Len(t, payload.Entries, 1)
	assertLocationEntry(t, payload.Entries[0], "33.749", "-84.388", "320.5", "4.2", "1.75", "271.5", occurred)
}

// Assert every field of a resolved location entry against its own expected value.
func assertLocationEntry(t *testing.T, got model.ResolvedLocationEntry,
	lat, lon, elev, acc, speed, heading string, occurred time.Time) {
	t.Helper()
	require.NotNil(t, got.Latitude, "latitude missing")
	assert.Equal(t, lat, *got.Latitude, "latitude")
	require.NotNil(t, got.Longitude, "longitude missing")
	assert.Equal(t, lon, *got.Longitude, "longitude")
	require.NotNil(t, got.Elevation, "elevation missing")
	assert.Equal(t, elev, *got.Elevation, "elevation")
	require.NotNil(t, got.Accuracy, "accuracy missing")
	assert.Equal(t, acc, *got.Accuracy, "accuracy")
	require.NotNil(t, got.Speed, "speed missing")
	assert.Equal(t, speed, *got.Speed, "speed")
	require.NotNil(t, got.Heading, "heading missing")
	assert.Equal(t, heading, *got.Heading, "heading")
	assert.True(t, got.OccurredTime.Equal(occurred), "occurred time: got %v want %v", got.OccurredTime, occurred)
}

// The geofence stamp (ADR-078) must survive the wire, or event-processing has no way to
// know which fence set an event was resolved against and geofence replay silently
// degrades to "whatever fences exist now".
//
// The measurement half is the anchor: with only the location assertion, a marshal that
// dropped the field entirely would still fail — but a marshal that stamped every event
// would pass, and stamping every event is the specific mistake this field's LOCATION-only
// rule exists to prevent.
func TestResolvedEventRoundTripCarriesFenceSetVersion(t *testing.T) {
	occurred := at(t, "2026-08-09T14:32:07.125Z")
	location := &model.ResolvedEvent{
		Source:            "test-source",
		SourceDeviceToken: "TEST-123",
		EventType:         esmodel.Location,
		FenceSetVersion:   19,
		Payload: &model.ResolvedLocationsPayload{
			Entries: []model.ResolvedLocationEntry{{Latitude: s("33.749"), Longitude: s("-84.388"), OccurredTime: occurred}},
		},
	}
	encoded, err := MarshalResolvedEvent(location)
	require.NoError(t, err)
	decoded, err := UnmarshalResolvedEvent(encoded)
	require.NoError(t, err)
	assert.Equal(t, int32(19), decoded.FenceSetVersion, "the fence set version did not survive the wire")

	measurement := &model.ResolvedEvent{
		Source:            "test-source",
		SourceDeviceToken: "TEST-123",
		EventType:         esmodel.Measurement,
		Payload: &model.ResolvedMeasurementsPayload{
			Entries: []model.ResolvedMeasurementsEntry{{
				Entries: []model.ResolvedMeasurementEntry{{Name: "temperature", Value: "42"}},
			}},
		},
	}
	mencoded, err := MarshalResolvedEvent(measurement)
	require.NoError(t, err)
	mdecoded, err := UnmarshalResolvedEvent(mencoded)
	require.NoError(t, err)
	assert.Equal(t, int32(0), mdecoded.FenceSetVersion, "a measurement event arrived carrying a fence set version")
}

// An entry published BEFORE per-sample times were honoured carries no time on the wire,
// and must decode at the message's own time — which is what it meant when it was written.
//
// 🔴 This is not a compatibility nicety, it is the replay preview's correctness. The
// preview reads historical bytes straight off the retained resolved-events stream, so
// without the fallback every event older than this change would either fail to decode
// (and be skipped as corrupt, silently shortening history) or decode at the zero instant
// and sort before everything. The payload is built as PROTO here rather than through the
// marshaller precisely because the marshaller can no longer produce this shape.
func TestAnEntryWithNoWireTimeDecodesAtTheEnvelopeTime(t *testing.T) {
	envelope := at(t, "2026-08-09T14:32:07.125Z")

	legacy := &PResolvedLocationsPayload{
		Entries: []*PResolvedLocationEntry{
			{Latitude: s("33.749"), Longitude: s("-84.388")}, // no OccurredTime on the wire
			{Latitude: s("47.6062"), Longitude: s("-122.3321"), OccurredTime: s("2026-08-09T14:32:09.500Z")},
		},
	}
	encoded, err := proto.Marshal(legacy)
	require.NoError(t, err)

	decoded, err := UnmarshalPayloadForLocationsEvent(encoded, envelope)
	require.NoError(t, err)
	require.Len(t, decoded.Entries, 2)

	assert.True(t, decoded.Entries[0].OccurredTime.Equal(envelope),
		"an entry with no wire time reads at the envelope's: got %v want %v",
		decoded.Entries[0].OccurredTime, envelope)
	// The sibling is the counterweight: a fallback that overwrote every entry with the
	// envelope would satisfy the assertion above and destroy the feature.
	assert.True(t, decoded.Entries[1].OccurredTime.Equal(at(t, "2026-08-09T14:32:09.500Z")),
		"an entry that DOES carry its own time must keep it: got %v", decoded.Entries[1].OccurredTime)
}

// A present-but-unparseable entry time FAILS, rather than quietly falling back. Absence
// is history; a malformed value is a defect in this repository, and swallowing it would
// collapse a batch onto one instant — the exact failure per-sample times remove.
func TestAMalformedWireTimeIsRefused(t *testing.T) {
	bad := &PResolvedLocationsPayload{
		Entries: []*PResolvedLocationEntry{
			{Latitude: s("33.749"), Longitude: s("-84.388"), OccurredTime: s("yesterday")},
		},
	}
	encoded, err := proto.Marshal(bad)
	require.NoError(t, err)

	_, err = UnmarshalPayloadForLocationsEvent(encoded, at(t, "2026-08-09T14:32:07.125Z"))
	require.Error(t, err, "a malformed entry time must not decode")
	assert.Contains(t, err.Error(), "yesterday", "the error must name the offending value")
}
