// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
)

// A location payload survives the protobuf hop with its VALUES intact.
//
// 🔴 The counterweight this file exists to provide. marshal_test.go already
// round-trips a location payload — with an EMPTY Entries slice. That test passes
// whether or not a single coordinate field is marshalled, which is how the
// unresolved location wire stayed unexercised for the life of the project. An
// empty-collection round trip proves the envelope, never the contents.
func TestLocationPayloadRoundTripsWithItsValues(t *testing.T) {
	s := func(v string) *string { return &v }

	// Distinct, non-round values throughout: six fields all set to "1" would let a
	// marshaller that wrote accuracy into speed pass unnoticed, and three fields
	// were added here at once, which is exactly when that mistake gets made.
	original := &model.UnresolvedLocationsPayload{
		Entries: []model.UnresolvedLocationEntry{
			{
				Latitude:     s("33.74900000"),
				Longitude:    s("-84.38800000"),
				Elevation:    s("320.5"),
				Accuracy:     s("4.2"),
				Speed:        s("1.75"),
				Heading:      s("271.5"),
				OccurredTime: s("2026-08-09T12:00:00.1234567Z"),
			},
			// A second entry with its own values: a loop that reuses one entry
			// variable satisfies a single-entry assertion and fails this one.
			{
				Latitude:  s("-33.86800000"),
				Longitude: s("151.20900000"),
			},
		},
	}

	encoded, err := MarshalPayloadForLocationsEvent(original)
	if err != nil {
		t.Fatalf("marshalling a locations payload: %v", err)
	}
	decoded, err := UnmarshalPayloadForLocationsEvent(encoded)
	if err != nil {
		t.Fatalf("unmarshalling a locations payload: %v", err)
	}
	if len(decoded.Entries) != 2 {
		t.Fatalf("got %d entries back, want 2", len(decoded.Entries))
	}

	same := func(field string, got, want *string) {
		t.Helper()
		switch {
		case want == nil && got != nil:
			t.Fatalf("%s: got %q, want nil — an unreported field must not be invented", field, *got)
		case want == nil:
			return
		case got == nil:
			t.Fatalf("%s: got nil, want %q", field, *want)
		case *got != *want:
			t.Fatalf("%s: got %q, want %q", field, *got, *want)
		}
	}
	for i, want := range original.Entries {
		got := decoded.Entries[i]
		same("latitude", got.Latitude, want.Latitude)
		same("longitude", got.Longitude, want.Longitude)
		same("elevation", got.Elevation, want.Elevation)
		same("accuracy", got.Accuracy, want.Accuracy)
		same("speed", got.Speed, want.Speed)
		same("heading", got.Heading, want.Heading)
		same("occurredTime", got.OccurredTime, want.OccurredTime)
	}
}
