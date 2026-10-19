// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"bytes"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// A calculated metric's value (ADR-082) crosses the resolved wire with its Derived flag, in
// both formats, and a raw entry is written exactly as it was before the flag existed: the
// flag is an implicit-presence bool, so it costs a raw entry no byte and a message written
// before it decodes with every entry raw (TestALegacyResolvedMessageDecodesToItsEvent).
func TestADerivedEntryCrossesTheWireInBothFormats(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	event := func(derived bool) *ResolvedEvent {
		return &ResolvedEvent{Source: "mqtt", SourceDeviceToken: "meter-1", EventType: eventtype.Measurement,
			OccurredTime: at, ProcessedTime: at, Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
				Entries: []ResolvedMeasurementEntry{
					{Name: "current", Value: "12.25", Classifier: u64p(2), DataType: strp("DOUBLE")},
					{Name: "power_kw", Value: "2.823625", Classifier: u64p(5), Unit: strp("kW"), DataType: strp("DOUBLE"), Derived: derived},
				},
				OccurredTime: at}}}}
	}
	for _, format := range bothFormats {
		withFlag, err := MarshalResolvedEventAs(event(true), format)
		require.NoError(t, err)
		without, err := MarshalResolvedEventAs(event(false), format)
		require.NoError(t, err)

		// The flag is exactly two bytes on the derived entry — its tag (field 6, varint) and
		// a 1 — plus the length prefixes that grow by them, and nothing on the raw entry.
		require.Equal(t, len(without)+2, len(withFlag), "%s", format)
		flag := protowire.AppendVarint(protowire.AppendTag(nil, 6, protowire.VarintType), 1)
		require.Equal(t, 1, bytes.Count(withFlag, flag), "%s: the flag is written once, on the derived entry", format)

		decoded, err := UnmarshalResolvedEvent(withFlag)
		require.NoError(t, err)
		require.Equal(t, event(true), decoded, "%s", format)
		raw, err := UnmarshalResolvedEvent(without)
		require.NoError(t, err)
		require.Equal(t, event(false), raw, "%s", format)
	}
}
