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
)

// 🔴 THE ROLLBACK GUARANTEE, for the resolved stream. v0.23.0 is the first release that reads
// the compact format; it wrote legacy by default and compact only when configured to. Its
// consumers read compact through the reader it shipped, which was tested against what its own
// compact writer produced. So a resolved event written by THIS release's default producer is
// readable by a v0.23.0 pod (a rollback, or a pod that has not rolled yet) exactly when it is
// what v0.23.0's compact writer wrote for the same event.
//
// These bytes were captured by running MarshalResolvedEventAs(event, eventtime.WireCompact) on
// the v0.23.0 tag (0132165d4) over goldenResolvedEvents and v023DerivedEvent. If one of them
// has to change, this release's default output is no longer v0.23.0's, and a rollback to
// v0.23.0 has to be proven again rather than assumed.
var v023CompactResolvedHex = map[string]string{
	"alert out of range":  "0a04687474701a03642d313a1e313530302d30312d30325430333a30343a30352e3030303030303030365a4223393939392d31322d33315432333a35393a35392e3939393939393939392d30353a30304803524f0a2b0a054641554c5410021a0570726f626522036465762a14323330302d30312d30315430303a30303a30305a0a100a0545504f43483100000000000000000a0e0a0350524531ffffffffffffffff",
	"derived measurement": "0a046d7174741a076d657465722d314802524d0a4b0a1a0a0763757272656e74120531322e323518022a06444f55424c450a240a08706f7765725f6b771208322e383233363235180522026b572a06444f55424c45300119008081afbf00df188901008081afbf00df189101008081afbf00df18",
	"location":            "0a046d7174741a0a73656e736f722d3030314801521c0a1a0a0434352e3512052d37332e363a0239304101fc2a7893f8de185a07747261636b6572620574726b403178048901159321bb93f8de189101471f39bb93f8de18",
	"measurement full":    "0a04687474701205616c742d311a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f1805220d0a04617265611203612d3118094802524a0a310a1b0a0474656d70120432312e351801220343656c2a06444f55424c450a090a0368756d1202343019b16409b393f8de180a150a0a0a0474656d70120232321901fc2a7893f8de185a0b64657669636570756c7365620770756c736540336a070a03672d31100272056578742d3178078001018901159321bb93f8de189101471f39bb93f8de18",
	"presence disabled":   "0a046d7174741a056465762d314214303030312d30312d30315430303a30303a30305a4804522d0a0c444953434f4e4e45435445441205636c6f736518072214323032362d31302d31365430393a33303a31355a620370403180010189010000000000000000",
}

// v023DerivedEvent carries a calculated metric's value, so the Derived flag is in the capture:
// v0.23.0 already wrote and read it, in both formats.
func v023DerivedEvent() *ResolvedEvent {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	return &ResolvedEvent{Source: "mqtt", SourceDeviceToken: "meter-1", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at, Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
		Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
			Entries: []ResolvedMeasurementEntry{
				{Name: "current", Value: "12.25", Classifier: u64p(2), DataType: strp("DOUBLE")},
				{Name: "power_kw", Value: "2.823625", Classifier: u64p(5), Unit: strp("kW"), DataType: strp("DOUBLE"), Derived: true},
			},
			OccurredTime: at}}}}
}

func v023ResolvedEvents() map[string]*ResolvedEvent {
	events := goldenResolvedEvents()
	events["derived measurement"] = v023DerivedEvent()
	return events
}

// What this release writes by default is, byte for byte, what v0.23.0's compact writer wrote.
// So a v0.23.0 consumer reads it, and a rollback to v0.23.0 after compact messages were
// written loses none of them.
func TestTheDefaultResolvedBytesAreWhatV023ReadsAsCompact(t *testing.T) {
	require.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat())
	events := v023ResolvedEvents()
	require.Len(t, v023CompactResolvedHex, len(events))
	for name, event := range events {
		got, err := MarshalResolvedEvent(event)
		require.NoError(t, err, name)
		require.Equal(t, v023CompactResolvedHex[name], hex.EncodeToString(got),
			"%s: the default bytes are not what v0.23.0 wrote as compact, so v0.23.0 reading them is unproven", name)
	}
}

// And the other direction: compact bytes a v0.23.0 producer wrote (one configured with
// wireFormat: compact) decode here to the event it wrote, the Derived flag included.
func TestV023CompactResolvedBytesDecode(t *testing.T) {
	for name, event := range v023ResolvedEvents() {
		encoded, err := hex.DecodeString(v023CompactResolvedHex[name])
		require.NoError(t, err, name)
		got, err := UnmarshalResolvedEvent(encoded)
		require.NoError(t, err, name)
		legacy, err := MarshalResolvedEventAs(event, eventtime.WireLegacy)
		require.NoError(t, err, name)
		want, err := UnmarshalResolvedEvent(legacy)
		require.NoError(t, err, name)
		require.Equal(t, want, got, "%s: v0.23.0's compact bytes and the legacy bytes decode differently", name)
		require.True(t, got.OccurredTime.Equal(event.OccurredTime), name)
		require.True(t, got.ProcessedTime.Equal(event.ProcessedTime), name)
	}
	got, err := UnmarshalResolvedEvent(mustHex(t, v023CompactResolvedHex["derived measurement"]))
	require.NoError(t, err)
	require.Equal(t, v023DerivedEvent(), got)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}
