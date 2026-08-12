// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
)

// The per-sample timestamp is parsed AT THE DOOR, and a value that is not an instant is
// a terminal decode failure rather than a silent substitution.
//
// 🔴 Why this matters more than it looks. Before per-sample times were honoured, an
// unparseable one was swallowed — device-state parsed it with `if err == nil` and simply
// kept the envelope's time on failure. So a device sending "2026-13-45T99:99:99Z" was
// answered 202, its readings were stored at the wrong instant, and nothing anywhere
// counted it. Rejecting at the decoder is what makes the failure REACH the device: a
// decode error is terminal on every transport by construction (HTTP answers 400, a broker
// transport dead-letters and settles), so there is no retry loop and no misfiling as a
// downstream API failure.
func TestAMalformedEntryTimeIsRejectedAtTheDoor(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "measurement",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[
				{"measurements":{"temperature":"21.5"},"occurredTime":"2026-08-09T12:00:00Z"},
				{"measurements":{"temperature":"22.5"},"occurredTime":"yesterday"}]}}`,
			// The INDEX is part of the contract: a device batching a hundred buffered
			// readings needs to be told which one its firmware got wrong.
			want: `measurement entry 1 field "occurredTime" is not an RFC3339 timestamp: "yesterday"`,
		},
		{
			name: "location",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[
				{"latitude":"33.749","longitude":"-84.388","occurredTime":"12:00"}]}}`,
			want: `location entry 0 field "occurredTime" is not an RFC3339 timestamp: "12:00"`,
		},
		{
			name: "alert",
			body: `{"device":"d1","eventType":"Alert","payload":{"entries":[
				{"type":"SENSOR_FAULT","level":3,"occurredTime":"2026-08-09"}]}}`,
			// A date with no time is the near-miss worth naming: it looks like a
			// timestamp, and RFC3339 refuses it.
			want: `alert entry 0 field "occurredTime" is not an RFC3339 timestamp: "2026-08-09"`,
		},
		{
			name: "a non-string time",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[
				{"measurements":{"t":"1"},"occurredTime":1754740800}]}}`,
			// An epoch number is the single most likely wrong shape, since that is what
			// most firmware holds internally.
			want: "measurement entry occurredTime must be an RFC3339 string",
		},
		{
			name: "the zero instant on an entry",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[
				{"measurements":{"t":"1"},"occurredTime":"0001-01-01T00:00:00Z"}]}}`,
			// 🔴 VALID RFC 3339 AND STILL REFUSED. It is exactly Go's zero time.Time, which
			// this platform uses as the sentinel for "no time was reported" — the historian's
			// fail-closed guard keys on it. Accepted here, the reading would resolve, project,
			// evaluate and stream, then be refused at the history write alone: every surface
			// would hold a reading the history table does not.
			want: "is the zero instant",
		},
		{
			name: "the zero instant on the envelope",
			body: `{"device":"d1","eventType":"Measurement","occurredTime":"0001-01-01T00:00:00Z",` +
				`"payload":{"entries":[{"measurements":{"t":"1"}}]}}`,
			// The envelope reaches the same guard through the entry-time fallback, so refusing
			// it on the entry alone would leave the identical hole one field away.
			want: "is the zero instant",
		},
		{
			name: "the envelope's own time",
			body: `{"device":"d1","eventType":"Measurement","occurredTime":"nope","payload":{"entries":[
				{"measurements":{"t":"1"}}]}}`,
			want: `envelope occurredTime is not an RFC3339 timestamp: "nope"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decoder.Decode([]byte(tc.body))
			if err == nil {
				t.Fatal("a malformed timestamp must fail the decode, not be substituted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error message:\n got: %v\nwant it to contain: %s", err, tc.want)
			}
			// Typed so the caller can count clock-shaped rejections apart from malformed
			// payloads — two different call-outs for an operator.
			if !errors.Is(err, ErrInvalidEventTime) {
				t.Fatalf("error is not an ErrInvalidEventTime: %v", err)
			}
		})
	}
}

// A well-formed per-sample time arrives PARSED and at full precision, and a sample that
// carries none arrives absent rather than defaulted.
//
// This is the counterweight to the rejection test above: refusing bad timestamps is only
// worth anything while good ones still pass untouched. Absence has to survive too — it is
// what tells the resolver to fall back to the envelope, so defaulting it here would answer
// a question the device never answered.
func TestAWellFormedEntryTimeSurvivesAndAbsenceStaysAbsent(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	_, payload, err := decoder.Decode([]byte(
		`{"device":"d1","eventType":"Measurement","payload":{"entries":[
			{"measurements":{"t":"1"},"occurredTime":"2026-08-09T12:00:00.123456789Z"},
			{"measurements":{"t":"2"}}]}}`))
	if err != nil {
		t.Fatalf("a well-formed body must decode: %v", err)
	}
	entries := payload.(*model.UnresolvedMeasurementsPayload).Entries
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].OccurredTime == nil {
		t.Fatal("a reported sample time was dropped")
	}
	// Nanoseconds, not seconds. The sub-second digits are the whole reason a sample's
	// time is carried separately from the message's — two readings a millisecond apart
	// are two readings.
	want := time.Date(2026, 8, 9, 12, 0, 0, 123456789, time.UTC)
	if !entries[0].OccurredTime.Equal(want) {
		t.Fatalf("sample time: got %v, want %v", *entries[0].OccurredTime, want)
	}
	if entries[1].OccurredTime != nil {
		t.Fatalf("an unreported sample time must stay absent; got %v", *entries[1].OccurredTime)
	}
}
