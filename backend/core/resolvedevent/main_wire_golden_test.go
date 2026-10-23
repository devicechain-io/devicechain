// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/require"
)

// testdata/main_wire_golden.json holds the bytes the resolved-event marshaller wrote for
// these fixtures BEFORE the contract moved out of device-management's model and proto packages
// into this one: captured by running MarshalResolvedEventAs (both wire formats) and
// MarshalFailedEvent on origin/main 45d5aa9e5. Every payload type is covered, which the
// per-format hex in wire_format_test.go is not (it has no state change or new relationship).
//
// The wire is a contract with pods still running the release before, so a move is only a move
// if these bytes did not change: marshalling must reproduce them exactly, and unmarshalling
// them must reproduce the fixture.

func str(s string) *string { return &s }

func mainWireFixtures() map[string]*ResolvedEvent {
	t1 := time.Date(2026, 7, 20, 10, 30, 15, 111222333, time.UTC)
	t2 := time.Date(2026, 7, 20, 10, 30, 16, 444555666, time.UTC)
	outOfRange := time.Date(1600, 1, 2, 3, 4, 5, 6, time.UTC) // the count cannot hold it: string form
	classifier := uint64(42)
	target := uint64(11)
	base := func(et eventtype.EventType, payload interface{}) *ResolvedEvent {
		return &ResolvedEvent{
			Source: "mqtt", AltId: str("alt-1"), SourceDeviceToken: "dev-001",
			Anchors: []ResolvedAnchor{
				{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 5},
				{AnchorType: "area", AnchorToken: "wh-3", RelationshipId: 8},
			},
			DeviceTypeToken: "sensor", ProfileVersionToken: "prof@3", ExternalId: "ext/1",
			ScopeMemberships: []GroupRef{{GroupToken: "g1", Version: 2}},
			FenceSetVersion:  7, SourceDeviceDisabled: true,
			OccurredTime: t1, ProcessedTime: t2, EventType: et, Payload: payload,
		}
	}
	return map[string]*ResolvedEvent{
		"measurements": base(eventtype.Measurement, &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
			{OccurredTime: t1, Entries: []ResolvedMeasurementEntry{
				{Name: "temp", Value: "21.5", Classifier: &classifier, Unit: str("Cel"), DataType: str("DOUBLE")},
				{Name: "calc", Value: "3", Derived: true}}},
			{OccurredTime: outOfRange, Entries: []ResolvedMeasurementEntry{{Name: "h", Value: "0"}}},
		}}),
		"locations": base(eventtype.Location, &ResolvedLocationsPayload{Entries: []ResolvedLocationEntry{
			{Latitude: str("1.5"), Longitude: str("-2.5"), Elevation: str("10"), Accuracy: str("3"), Speed: str("4"), Heading: str("90"), OccurredTime: t1},
			{Latitude: str("0"), Longitude: str("0"), OccurredTime: outOfRange}}}),
		"alerts": base(eventtype.Alert, &ResolvedAlertsPayload{Entries: []ResolvedAlertEntry{
			{Type: "overheat", Level: 3, Message: "hot", Source: "s", OccurredTime: t2}}}),
		"statechange": base(eventtype.StateChange, &ResolvedStateChangePayload{
			State: "CONNECTED", Reason: "reg", ExpectedSessionId: 4, SessionId: 9, OccurredTime: str("2026-10-16T09:30:15Z")}),
		"newrelationship": base(eventtype.NewRelationship, &ResolvedNewRelationshipPayload{
			RelationshipTypeId: 3, TargetType: str("asset"), TargetId: &target}),
		"bare": {Source: "s", SourceDeviceToken: "d", OccurredTime: t1, ProcessedTime: t1,
			EventType: eventtype.Measurement, Payload: &ResolvedMeasurementsPayload{}},
	}
}

func TestResolvedWireMatchesTheBytesWrittenBeforeTheMove(t *testing.T) {
	raw, err := os.ReadFile("testdata/main_wire_golden.json")
	require.NoError(t, err)
	var golden map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &golden))

	formats := map[string]eventtime.WireFormat{"compact": eventtime.WireCompact, "legacy": eventtime.WireLegacy}
	for name, event := range mainWireFixtures() {
		for formatName, format := range formats {
			want, err := hex.DecodeString(golden[name][formatName])
			require.NoError(t, err, name)
			require.NotEmpty(t, want, "%s/%s has no golden bytes", name, formatName)

			got, err := MarshalResolvedEventAs(event, format)
			require.NoError(t, err, name)
			require.Equal(t, want, got, "%s/%s: the wire bytes moved", name, formatName)

			decoded, err := UnmarshalResolvedEvent(want)
			require.NoError(t, err, name)
			// The decoded form round-trips to the same bytes, whatever in-memory nil-versus-
			// empty differences it carries.
			again, err := MarshalResolvedEventAs(decoded, format)
			require.NoError(t, err, name)
			require.Equal(t, want, again, "%s/%s: decode then encode is not the identity", name, formatName)
		}
	}

	failed, err := hex.DecodeString(golden["failed"]["compact"])
	require.NoError(t, err)
	got, err := MarshalFailedEvent(&FailedEvent{Reason: 3, Service: "svc", Message: "m", Error: "e", Payload: []byte{1, 2, 3}})
	require.NoError(t, err)
	require.Equal(t, failed, got)
}

// A gateway token on a resolved event adds exactly one field to the bytes the release before
// wrote, at the END (field 19, so its tag is two bytes: 0x9a 0x01), and an event without one is
// byte-for-byte what it was (the golden test above). The expectation is built from the existing
// golden bytes plus the hand-encoded field, not from this writer's own output, so it cannot be
// satisfied by a writer that is wrong in the same way twice.
func TestGatewayTokenAppendsOneFieldAndNothingElseMoves(t *testing.T) {
	raw, err := os.ReadFile("testdata/main_wire_golden.json")
	require.NoError(t, err)
	var golden map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &golden))

	event := mainWireFixtures()["bare"]
	event.GatewayToken = "gw1"
	field := []byte{0x9a, 0x01, 0x03, 'g', 'w', '1'}

	for formatName, format := range map[string]eventtime.WireFormat{"compact": eventtime.WireCompact, "legacy": eventtime.WireLegacy} {
		base, err := hex.DecodeString(golden["bare"][formatName])
		require.NoError(t, err)
		want := append(append([]byte(nil), base...), field...)

		got, err := MarshalResolvedEventAs(event, format)
		require.NoError(t, err)
		require.Equal(t, want, got, "%s: a gateway token must append only field 19", formatName)

		decoded, err := UnmarshalResolvedEvent(want)
		require.NoError(t, err)
		require.Equal(t, "gw1", decoded.GatewayToken, "%s: the decoder lost the gateway token", formatName)
		require.Equal(t, "d", decoded.SourceDeviceToken, "%s: attribution stays the child", formatName)
	}
}
