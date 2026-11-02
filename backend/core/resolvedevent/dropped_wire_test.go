// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/require"
)

// A drop record crosses the resolved wire in both formats, and a sample that lost nothing is
// written exactly as it was before the field existed: a repeated field costs an empty list no
// byte.
func TestADroppedEntryCrossesTheWireInBothFormats(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	event := func(dropped []DroppedMeasurementEntry) *ResolvedEvent {
		return &ResolvedEvent{Source: "mqtt", SourceDeviceToken: "d-1", EventType: eventtype.Measurement,
			OccurredTime: at, ProcessedTime: at, Anchors: []ResolvedAnchor{}, ScopeMemberships: []GroupRef{},
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
				Entries:      []ResolvedMeasurementEntry{{Name: "battery", Value: "50"}},
				OccurredTime: at, Dropped: dropped}}}}
	}
	drops := []DroppedMeasurementEntry{
		{Name: "status_text", Reason: DropUndeclaredNonNumeric, Value: "ok"},
		{Name: "huge", Reason: DropUndeclaredUnstorable, Value: "1e30"},
	}
	for _, format := range bothFormats {
		with, err := MarshalResolvedEventAs(event(drops), format)
		require.NoError(t, err)
		without, err := MarshalResolvedEventAs(event(nil), format)
		require.NoError(t, err)
		require.Greater(t, len(with), len(without), "%s", format)

		decoded, err := UnmarshalResolvedEvent(with)
		require.NoError(t, err)
		require.Equal(t, event(drops), decoded, "%s", format)
		plain, err := UnmarshalResolvedEvent(without)
		require.NoError(t, err)
		require.Equal(t, event(nil), plain, "%s", format)
		require.Nil(t, plain.Payload.(*ResolvedMeasurementsPayload).Entries[0].Dropped)
	}

	// Negative control: a drop whose text is not UTF-8 is refused like any other string.
	bad := event([]DroppedMeasurementEntry{{Name: "k", Reason: DropUndeclaredNonNumeric, Value: "\xff"}})
	_, err := MarshalResolvedEventAs(bad, bothFormats[0])
	require.ErrorIs(t, err, errResolvedNotUTF8)
}

// The identity view removes a drop record, leaves the payload itself alone, and a payload with
// none is its own view. The payload's JSON (what event identity hashes) is byte-identical with
// and without a drop only through the view.
func TestDroppedEntriesAreOutsideTheIdentityView(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	mk := func(dropped ...DroppedMeasurementEntry) *ResolvedMeasurementsPayload {
		return &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
			Entries:      []ResolvedMeasurementEntry{{Name: "battery", Value: "50"}},
			OccurredTime: at, Dropped: dropped}}}
	}
	clean := mk()
	require.Same(t, clean, clean.IdentityView())

	dropped := mk(DroppedMeasurementEntry{Name: "status_text", Reason: DropUndeclaredNonNumeric, Value: "ok"})
	view := dropped.IdentityView()
	require.NotSame(t, dropped, view)
	require.Len(t, dropped.Entries[0].Dropped, 1, "the view must not strip the payload in place")

	enc := func(p *ResolvedMeasurementsPayload) string {
		b, err := json.Marshal(p)
		require.NoError(t, err)
		return string(b)
	}
	require.Equal(t, enc(clean), enc(view))
	// Negative control: hashed whole, the drop is visible, so the view is what hides it.
	require.NotEqual(t, enc(clean), enc(dropped))
	require.NotContains(t, enc(clean), "Dropped", "omitempty keeps a clean sample's document unchanged")

	// An all-dropped sample keeps its place and time, empty, in the view.
	all := &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{{
		Entries: []ResolvedMeasurementEntry{}, OccurredTime: at,
		Dropped: []DroppedMeasurementEntry{{Name: "s", Reason: DropUndeclaredNonNumeric, Value: "x"}}}}}
	require.Equal(t, `{"Entries":[{"Entries":[],"OccurredTime":"2026-10-16T12:00:00Z"}]}`, enc(all.IdentityView()))
}

func TestTruncateDroppedValue(t *testing.T) {
	require.Equal(t, "ok", TruncateDroppedValue("ok"))
	exact := strings.Repeat("a", MaxDroppedValueBytes)
	require.Equal(t, exact, TruncateDroppedValue(exact))
	require.Equal(t, exact, TruncateDroppedValue(exact+"b"))
	// A cut inside a multi-byte rune backs up to the rune's start: the result is valid UTF-8.
	s := strings.Repeat("a", MaxDroppedValueBytes-1) + "é"
	got := TruncateDroppedValue(s)
	require.Equal(t, strings.Repeat("a", MaxDroppedValueBytes-1), got)
	require.True(t, utf8.ValidString(got))
	// Negative control: a naive byte slice of the same string is the invalid one.
	require.False(t, utf8.ValidString(s[:MaxDroppedValueBytes]))
}
