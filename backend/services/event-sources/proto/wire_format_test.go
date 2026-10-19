// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"encoding/hex"
	"github.com/devicechain-io/dc-microservice/presence"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 🔴 THE ROLLING-UPGRADE GUARANTEE. A pod still on the release before the compact format
// reads only what that release wrote. So the LEGACY format this release writes by default
// must be that, byte for byte: not "equivalent", not "decodes the same", but the same bytes,
// for every shape a producer sends. These are those bytes, captured by running
// MarshalUnresolvedEvent on origin/main 9b680acf6 over goldenInboundEvents (the seals are
// text-form seals that release made, under the root key of 32 bytes 0x09).
var mainInboundHex = map[string]string{
	"alert out of range": "0a04687474701a03642d312a1e313530302d30312d30325430333a30343a30352e3030303030303030365a3223393939392d31322d33315432333a35393a35392e3939393939393939392d30353a30303803426d0a250a054641554c5410031a016d2201732a14323330302d30312d30315430303a30303a30305a0a1d0a0545504f43482a14313937302d30312d30315430303a30303a30305a0a250a035052452a1e313936392d31322d33315432333a35393a35392e3939393939393939395a6001",
	"password location":  "0a046d7174741205616c742d311a0873656e736f722d39220372656c2a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a3801423b0a310a0434352e3512052d37332e361a023130221e323032362d31302d31365430393a33303a31342e3030303030303030315a0a060a01311201324a0a4d5154545f4241534943520873656e736f722d395a50646373322465323936613065612473486c453565514779595a556a37664776514c6b30332b6b554754516c686936462b51476f35475570672b664f416a61507139655572504955324e524272322f6a41",
	"presence zero time": "0a056c776d326d1a03642d322a14303030312d30312d30315430303a30303a30305a3214303030312d30312d30315430303a30303a30305a380442290a09434f4e4e454354454412037265671a01372214323032362d31302d31365430393a33303a31355a6001",
	"relationship":       "0a04687474701a03642d332a23323032362d31302d31365430343a33303a31352e3132353030303030372d30353a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a42170a08636f6e7461696e7312066465766963651a03642d34",
	"token measurement":  "0a04687474701a1164657669636570756c73652d30303030312a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a3802426b0a2e0a0c0a0474656d70120432312e35121e323032362d31302d31365430393a33303a31342e3938373635343332315a0a0b0a090a0368756d120234300a2c0a0a0a0474656d7012023232121e323032362d31302d31365430393a33303a31342e3030303030303030315a4a0c4143434553535f544f4b454e6a6064637332246532393661306561246b3275564d59314c77346b6b3457354d5a6e59456d446e56444149484c46562f564f47474c4d5751426d614771564835755935485458552f4f314b424a53454552715a466233627868444678723761544467",
}

// The text-form seals in mainInboundHex, as origin/main made them.
const (
	mainTokenSealText    = "dcs2$e296a0ea$k2uVMY1Lw4kk4W5MZnYEmDnVDAIHLFV/VOGGLMWQBmaGqVH5uY5HTXU/O1KBJSEERqZFb3bxhDFxr7aTDg"
	mainPasswordSealText = "dcs2$e296a0ea$sHlE5eQGyYZUj7fGvQLk03+kUGTQlhi6F+QGo5GUpg+fOAjaPq9eUrPIU2NRBr2/jA"
)

func gsp(s string) *string                { return &s }
func gtp(t time.Time) *time.Time          { return &t }
func goldenZone(hours int) *time.Location { return time.FixedZone("", hours*3600) }

// goldenInboundEvents is, field for field, the input the origin/main capture ran on. The one
// difference is the type of the seal: this release holds it in binary, converted from the
// text form without re-sealing.
func goldenInboundEvents() map[string]*model.UnresolvedEvent {
	occ := time.Date(2026, 10, 16, 11, 30, 15, 123456789, goldenZone(2))
	proc := time.Date(2026, 10, 16, 9, 30, 15, 125000007, time.UTC)
	e1 := time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)
	e2 := time.Date(2026, 10, 16, 4, 30, 14, 1, goldenZone(-5))
	return map[string]*model.UnresolvedEvent{
		"token measurement": {Source: "http", Device: "devicepulse-00001", OccurredTime: occ, ProcessedTime: proc,
			EventType: model.Measurement, CredentialType: gsp("ACCESS_TOKEN"), SealedCredentialId: credential.SealedFromText(mainTokenSealText),
			Payload: &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"temp": "21.5"}, OccurredTime: gtp(e1)},
				{Measurements: map[string]string{"hum": "40"}},
				{Measurements: map[string]string{"temp": "22"}, OccurredTime: gtp(e2)},
			}}},
		"password location": {Source: "mqtt", AltId: gsp("alt-1"), Device: "sensor-9", Relationship: gsp("rel"),
			OccurredTime: occ, ProcessedTime: proc, EventType: model.Location,
			CredentialType: gsp("MQTT_BASIC"), CredentialId: gsp("sensor-9"), SealedCredentialSecret: credential.SealedFromText(mainPasswordSealText),
			Payload: &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{
				{Latitude: gsp("45.5"), Longitude: gsp("-73.6"), Elevation: gsp("10"), OccurredTime: gtp(e2)},
				{Latitude: gsp("1"), Longitude: gsp("2")},
			}}},
		"alert out of range": {Source: "http", Device: "d-1",
			OccurredTime: time.Date(1500, 1, 2, 3, 4, 5, 6, time.UTC), ProcessedTime: time.Date(9999, 12, 31, 23, 59, 59, 999999999, goldenZone(-5)),
			EventType: model.Alert, AuthenticatedTransport: true,
			Payload: &model.UnresolvedAlertsPayload{Entries: []model.UnresolvedAlertEntry{
				{Type: "FAULT", Level: 3, Message: "m", Source: "s", OccurredTime: gtp(time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC))},
				{Type: "EPOCH", OccurredTime: gtp(time.Unix(0, 0))},
				{Type: "PRE", OccurredTime: gtp(time.Unix(-1, 999999999))},
			}}},
		"presence zero time": {Source: "lwm2m", Device: "d-2", EventType: model.StateChange, AuthenticatedTransport: true,
			Payload: &model.UnresolvedStateChangePayload{State: presence.StateConnected, Reason: "reg", SessionId: "7", OccurredTime: gsp("2026-10-16T09:30:15Z")}},
		"relationship": {Source: "http", Device: "d-3", OccurredTime: proc.In(goldenZone(-5)), ProcessedTime: proc, EventType: model.NewRelationship,
			Payload: &model.UnresolvedNewRelationshipPayload{RelationshipType: "contains", TargetType: "device", Target: "d-4"}},
	}
}

func TestTheLegacyFormatIsByteForByteWhatTheReleaseBeforeWrote(t *testing.T) {
	events := goldenInboundEvents()
	require.Len(t, mainInboundHex, len(events))
	for name, event := range events {
		want, err := hex.DecodeString(mainInboundHex[name])
		require.NoError(t, err, name)
		got, err := MarshalUnresolvedEventAs(event, eventtime.WireLegacy)
		require.NoError(t, err, name)
		require.Equal(t, hex.EncodeToString(want), hex.EncodeToString(got), "%s: legacy bytes differ from origin/main's", name)
	}
}

// The process default writes the compact format: an instance that sets nothing writes what
// MarshalUnresolvedEventAs(compact) writes, and not the legacy bytes.
func TestTheDefaultProducerFormatIsTheCompactFormat(t *testing.T) {
	require.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat())
	differ := 0
	for name, event := range goldenInboundEvents() {
		compact, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
		require.NoError(t, err, name)
		got, err := MarshalUnresolvedEvent(event)
		require.NoError(t, err, name)
		require.True(t, bytes.Equal(compact, got), name)
		// An event with no instant or seal to encode is the same in both formats.
		if legacy, _ := hex.DecodeString(mainInboundHex[name]); !bytes.Equal(legacy, got) {
			differ++
		}
	}
	require.NotZero(t, differ, "every golden event's default bytes equal its legacy bytes")
}

// oldReleaseDecode is what a consumer on the release before reads off an inbound message:
// the string instants, parsed, and the text-form seals. Nothing else of the envelope's time
// or seal exists for it. It refuses a message it cannot read, as that release did.
func oldReleaseDecode(t *testing.T, encoded []byte) (occurred, processed time.Time, secret, token *string, err error) {
	t.Helper()
	var pb PUnresolvedEvent
	require.NoError(t, proto.Unmarshal(encoded, &pb))
	if occurred, err = time.Parse(time.RFC3339Nano, pb.OccurredTime); err != nil {
		return
	}
	if processed, err = time.Parse(time.RFC3339Nano, pb.ProcessedTime); err != nil {
		return
	}
	return occurred, processed, pb.CredentialSecret, pb.SealedCredentialId, nil
}

// The legacy format is readable by the release before; the compact one is not, which is
// exactly the window the default closes. Checked through the old reader's own logic: the
// legacy message's instants and seals arrive, and its seal opens under the old reader's
// path; the compact message's envelope instants are not in a field the old reader knows.
func TestOnlyTheLegacyFormatIsReadableByTheReleaseBefore(t *testing.T) {
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	event := goldenInboundEvents()["token measurement"]

	legacy, err := MarshalUnresolvedEventAs(event, eventtime.WireLegacy)
	require.NoError(t, err)
	occurred, processed, secret, token, err := oldReleaseDecode(t, legacy)
	require.NoError(t, err)
	require.True(t, occurred.Equal(event.OccurredTime))
	require.True(t, processed.Equal(event.ProcessedTime))
	require.Nil(t, secret)
	require.NotNil(t, token)
	opened, err := tr.OpenToken("acme", "ACCESS_TOKEN", credential.SealedFromText(*token))
	require.NoError(t, err)
	require.Equal(t, "tok-golden-0123456789", opened)

	var pb PUnresolvedEvent
	require.NoError(t, proto.Unmarshal(legacy, &pb))
	require.Nil(t, pb.Occurred, "legacy writes no count")
	require.Nil(t, pb.Processed)
	require.Nil(t, pb.SealedSecret, "legacy writes no binary seal")
	require.Nil(t, pb.SealedToken)

	compact, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	require.NoError(t, err)
	_, _, _, _, err = oldReleaseDecode(t, compact)
	require.Error(t, err, "the release before cannot read a compact envelope: that is the loss window")
}

// Both formats read back, through this release's reader, to the same event: every instant to
// the nanosecond, every seal opening. The format is the producer's choice only.
func TestBothFormatsDecodeToTheSameEvent(t *testing.T) {
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	for name, event := range goldenInboundEvents() {
		legacy, err := MarshalUnresolvedEventAs(event, eventtime.WireLegacy)
		require.NoError(t, err, name)
		compact, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
		require.NoError(t, err, name)
		require.LessOrEqual(t, len(compact), len(legacy), name)
		fromLegacy, err := UnmarshalUnresolvedEvent(legacy)
		require.NoError(t, err, name)
		fromCompact, err := UnmarshalUnresolvedEvent(compact)
		require.NoError(t, err, name)
		require.Equal(t, fromLegacy, fromCompact, name)
		require.True(t, fromLegacy.OccurredTime.Equal(event.OccurredTime), name)
		require.True(t, fromLegacy.ProcessedTime.Equal(event.ProcessedTime), name)
		require.Equal(t, event.SealedCredentialId, fromCompact.SealedCredentialId, name)
		require.Equal(t, event.SealedCredentialSecret, fromCompact.SealedCredentialSecret, name)
	}
	ev := goldenInboundEvents()["password location"]
	for _, format := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
		encoded, err := MarshalUnresolvedEventAs(ev, format)
		require.NoError(t, err)
		decoded, err := UnmarshalUnresolvedEvent(encoded)
		require.NoError(t, err)
		pw, err := tr.Open("acme", "sensor-9", decoded.SealedCredentialSecret)
		require.NoError(t, err, format)
		require.Equal(t, "pw-golden", pw, format)
	}
}

// Every instant survives both formats to the nanosecond, across the epoch, an offset, both
// ends of the count's range, past them, and the zero time.
func TestInboundInstantsRoundTripInBothFormats(t *testing.T) {
	for _, format := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
		for _, at := range []time.Time{
			time.Date(2026, 10, 16, 9, 30, 15, 1, time.UTC),
			time.Date(2026, 10, 16, 9, 30, 15, 999999999, goldenZone(-5)),
			time.Date(1969, 7, 20, 20, 17, 40, 123456789, time.UTC),
			time.Unix(0, 0),
			time.Unix(0, -9223372036854775808),
			time.Unix(0, 9223372036854775807),
			time.Date(9999, 12, 31, 23, 59, 59, 999999990, time.UTC),
			{},
		} {
			entryAt := at.Add(-3)
			ev := &model.UnresolvedEvent{Source: "s", Device: "d", OccurredTime: at, ProcessedTime: at.Add(7),
				EventType: model.Alert,
				Payload: &model.UnresolvedAlertsPayload{Entries: []model.UnresolvedAlertEntry{
					{Type: "t", OccurredTime: &entryAt}}}}
			encoded, err := MarshalUnresolvedEventAs(ev, format)
			require.NoError(t, err)
			got, err := UnmarshalUnresolvedEvent(encoded)
			require.NoError(t, err)
			require.True(t, got.OccurredTime.Equal(at), "%s: %s came back as %s", format, at, got.OccurredTime)
			require.True(t, got.ProcessedTime.Equal(at.Add(7)), "%s: %s", format, at)
			entry := got.Payload.(*model.UnresolvedAlertsPayload).Entries[0].OccurredTime
			require.True(t, entry.Equal(entryAt), "%s: entry %s came back as %s", format, entryAt, entry)
		}
	}
}

// A format that is neither is refused before anything is written: the zero value included,
// so a producer that was never told its format cannot guess one.
func TestAnUnknownWireFormatIsRefused(t *testing.T) {
	ev := goldenInboundEvents()["token measurement"]
	for _, format := range []eventtime.WireFormat{0, 3, 255} {
		// Every event type, including those whose payload carries no instant of its own,
		// so the refusal is the envelope's and not only a payload marshaller's.
		for name, each := range goldenInboundEvents() {
			_, err := MarshalUnresolvedEventAs(each, format)
			require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat, "%s: %d", name, format)
		}
		_, err := MarshalUnresolvedEventAs(ev, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat, "%d", format)
		_, err = MarshalPayloadForMeasurementsEvent(ev.Payload.(*model.UnresolvedMeasurementsPayload), format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat, "%d", format)
		_, err = MarshalPayloadForLocationsEvent(&model.UnresolvedLocationsPayload{}, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat, "%d", format)
		_, err = MarshalPayloadForAlertsEvent(&model.UnresolvedAlertsPayload{}, format)
		require.ErrorIs(t, err, eventtime.ErrUnknownWireFormat, "%d", format)
	}
}

// A seal that is not in the binary form cannot be written as legacy text: it is refused,
// never written as a text seal that would not open. (A producer only ever writes seals it
// made; this is the defect guard.)
func TestALegacyWriteRefusesASealThatIsNotBinary(t *testing.T) {
	ev := goldenInboundEvents()["token measurement"]
	ev.SealedCredentialId = []byte("tok-plaintext")
	_, err := MarshalUnresolvedEventAs(ev, eventtime.WireLegacy)
	require.ErrorIs(t, err, credential.ErrSealedSecretInvalid)

	ev = goldenInboundEvents()["password location"]
	ev.SealedCredentialSecret = []byte{}
	_, err = MarshalUnresolvedEventAs(ev, eventtime.WireLegacy)
	require.ErrorIs(t, err, credential.ErrSealedSecretInvalid)
}
