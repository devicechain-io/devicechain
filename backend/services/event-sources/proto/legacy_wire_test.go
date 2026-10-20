// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The inbound-events stream is RETAINED, so the resolver of a release that writes each
// instant as a count and each seal in binary still reads messages written before: across an
// upgrade, every message an ingest source published and the resolver had not yet consumed.
// These are such messages, byte for byte, written by MarshalUnresolvedEvent as it stood
// before (origin/main 9b680acf6). The first presents an ACCESS_TOKEN sealed in the text
// form, the second an MQTT password sealed in the text form, both under the root key
// legacyTransit derives, for tenant "acme". Their envelopes carry the device's +02:00
// offset, which the string form kept and the count does not.
var legacyInboundHex = map[string]string{
	"token measurement": "0a04687474701a1164657669636570756c73652d30303030312a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a380242300a2e0a0c0a0474656d70120432312e35121e323032362d31302d31365430393a33303a31342e3938373635343332315a4a0c4143434553535f544f4b454e6a686463733224653239366130656124556542543175674744684a354c34377866503257767a682f6d454166674837614d413865797a6530696a4a464c65595061436536445071477a69644852412b5467765768744533346d30475a475458677466524f4e2f68535377",
	"password location": "0a046d7174741a0a73656e736f722d3030312a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a380142370a2d0a0434352e3512052d37332e36221e323032362d31302d31365430393a33303a31342e3938373635343332315a0a060a01311201324a0a4d5154545f4241534943520a73656e736f722d3030315a5864637332246532393661306561244e4d48704c56442b33614247712f4c627937787339615546546a6a776b484444444551396844666d305853492f5a655833613376505747327835597a5076306d78762f58647a65304367",
}

func legacyTransit(t *testing.T) *credential.SecretTransit {
	t.Helper()
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return tr
}

var (
	legacyOccurred  = time.Date(2026, 10, 16, 9, 30, 15, 123456789, time.UTC)
	legacyProcessed = time.Date(2026, 10, 16, 9, 30, 15, 125000007, time.UTC)
	legacyEntry     = time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)
)

// currentInboundSize is each legacy message's size written in the current format: three
// instants as counts (35-, 30- and 30-byte strings, each with a tag and a length, become
// eight bytes and a tag), and the seal in binary at a two-byte-tag field. The token seal was
// 104 characters of text (dcs2$, 8 hex, $, 90 base64) and is 72 bytes (1 + 4 + 67); the
// password seal 88 characters and 60 bytes (1 + 4 + 55).
var currentInboundSize = map[string]int{
	"token measurement": 266 - 37 - 32 - 32 + 9 + 9 + 9 - (1 + 1 + 104) + (2 + 1 + 72), // 161
	"password location": 260 - 37 - 32 - 32 + 9 + 9 + 9 - (1 + 1 + 88) + (2 + 1 + 60),  // 159
}

// A message written before the count and the binary seal existed decodes to the event it was
// written for, every instant to the nanosecond, and its text-form seal opens as it did.
func TestALegacyInboundMessageDecodesAndItsSealOpens(t *testing.T) {
	tr := legacyTransit(t)

	raw, err := hex.DecodeString(legacyInboundHex["token measurement"])
	require.NoError(t, err)
	got, err := UnmarshalUnresolvedEvent(raw)
	require.NoError(t, err)
	require.Equal(t, "devicepulse-00001", got.Device)
	require.Equal(t, legacyOccurred, got.OccurredTime)
	require.Equal(t, legacyProcessed, got.ProcessedTime)
	mx := got.Payload.(*model.UnresolvedMeasurementsPayload)
	require.Len(t, mx.Entries, 1)
	require.Equal(t, map[string]string{"temp": "21.5"}, mx.Entries[0].Measurements)
	require.Equal(t, legacyEntry, *mx.Entries[0].OccurredTime)
	require.Nil(t, got.CredentialId)
	require.Nil(t, got.SealedCredentialSecret)
	token, err := tr.OpenToken("acme", "ACCESS_TOKEN", got.SealedCredentialId)
	require.NoError(t, err)
	require.Equal(t, "tok-legacy-0123456789abcdef", token)

	raw, err = hex.DecodeString(legacyInboundHex["password location"])
	require.NoError(t, err)
	got, err = UnmarshalUnresolvedEvent(raw)
	require.NoError(t, err)
	require.Equal(t, legacyOccurred, got.OccurredTime)
	loc := got.Payload.(*model.UnresolvedLocationsPayload)
	require.Len(t, loc.Entries, 2)
	require.Equal(t, legacyEntry, *loc.Entries[0].OccurredTime)
	require.Nil(t, loc.Entries[1].OccurredTime, "an entry the device did not time stays untimed")
	require.Equal(t, "sensor-001", *got.CredentialId)
	require.Nil(t, got.SealedCredentialId)
	secret, err := tr.Open("acme", "sensor-001", got.SealedCredentialSecret)
	require.NoError(t, err)
	require.Equal(t, "pw-legacy-value", secret)
}

// The same event written now is smaller by exactly what the counts and the binary seal save,
// writes neither the string instants nor the text seal, and decodes to what the legacy
// message decodes to.
func TestTheCurrentFormatCarriesTheLegacyInboundEventInFewerBytes(t *testing.T) {
	tr := legacyTransit(t)
	for name, h := range legacyInboundHex {
		raw, err := hex.DecodeString(h)
		require.NoError(t, err, name)
		fromLegacy, err := UnmarshalUnresolvedEvent(raw)
		require.NoError(t, err, name)

		current, err := MarshalUnresolvedEventAs(fromLegacy, eventtime.WireCompact)
		require.NoError(t, err, name)
		require.Equal(t, currentInboundSize[name], len(current), "%s: written in %d bytes (legacy %d)", name, len(current), len(raw))

		var pb PUnresolvedEvent
		require.NoError(t, proto.Unmarshal(current, &pb), name)
		require.Empty(t, pb.OccurredTime, name)
		require.Empty(t, pb.ProcessedTime, name)
		require.Nil(t, pb.CredentialSecret, "%s: the text seal field is read, never written", name)
		require.Nil(t, pb.SealedCredentialId, "%s: the text seal field is read, never written", name)

		fromCurrent, err := UnmarshalUnresolvedEvent(current)
		require.NoError(t, err, name)
		require.Equal(t, fromLegacy, fromCurrent, name)
	}

	// The binary seal the current format carries is the one that opens.
	raw, _ := hex.DecodeString(legacyInboundHex["token measurement"])
	ev, _ := UnmarshalUnresolvedEvent(raw)
	current, err := MarshalUnresolvedEventAs(ev, eventtime.WireCompact)
	require.NoError(t, err)
	var pb PUnresolvedEvent
	require.NoError(t, proto.Unmarshal(current, &pb))
	token, err := tr.OpenToken("acme", "ACCESS_TOKEN", pb.SealedToken)
	require.NoError(t, err)
	require.Equal(t, "tok-legacy-0123456789abcdef", token)
}

// A message carrying an envelope instant in neither form is refused, as one with an
// unparseable string always was: an absent instant is never read as some default.
func TestAnInboundMessageWithNoInstantIsRefused(t *testing.T) {
	nanos := int64(1)
	for name, pb := range map[string]*PUnresolvedEvent{
		"no occurred":  {SourceId: "s", Device: "d", Processed: &nanos},
		"no processed": {SourceId: "s", Device: "d", Occurred: &nanos},
	} {
		pb.EventType = int64(eventtype.NewRelationship)
		raw, err := proto.Marshal(pb)
		require.NoError(t, err, name)
		_, err = UnmarshalUnresolvedEvent(raw)
		require.Error(t, err, name)
	}
}

// Every instant survives to the nanosecond, including one before the epoch; and one past
// what the count holds survives in the string form.
func TestInboundInstantsRoundTripToTheNanosecond(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 10, 16, 9, 30, 15, 1, time.UTC),
		time.Date(2026, 10, 16, 9, 30, 15, 999999999, time.FixedZone("y", -5*3600)),
		time.Date(1969, 7, 20, 20, 17, 40, 123456789, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999990, time.UTC),
		{},
	} {
		entryAt := at.Add(-3)
		ev := &model.UnresolvedEvent{Source: "s", Device: "d", OccurredTime: at, ProcessedTime: at.Add(7),
			EventType: eventtype.Measurement,
			Payload: &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"a": "1"}, OccurredTime: &entryAt}}}}
		encoded, err := MarshalUnresolvedEventAs(ev, eventtime.WireCompact)
		require.NoError(t, err)
		got, err := UnmarshalUnresolvedEvent(encoded)
		require.NoError(t, err)
		require.True(t, got.OccurredTime.Equal(at), "%s came back as %s", at, got.OccurredTime)
		require.True(t, got.ProcessedTime.Equal(at.Add(7)), "%s", at)
		entry := got.Payload.(*model.UnresolvedMeasurementsPayload).Entries[0].OccurredTime
		require.True(t, entry.Equal(entryAt), "%s: entry came back as %s", at, entry)
	}
}
