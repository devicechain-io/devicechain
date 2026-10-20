// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reading's instant is nanosecond-precise and may carry a zone offset while it is live,
// and microsecond UTC once stored. The derived payload id and the stored occurred_time both
// go through CanonicalEntryInstant, so the id a row is built with is the id recomputed from
// the row read back. Each fixture below is the SAME instant in two spellings.
var canonicalFixtures = map[string]struct{ live, canonical time.Time }{
	"ns UTC": {
		live:      time.Date(2026, 10, 19, 10, 0, 0, 123456789, time.UTC),
		canonical: time.Date(2026, 10, 19, 10, 0, 0, 123456000, time.UTC),
	},
	"ns +02:00": {
		live:      time.Date(2026, 10, 19, 12, 0, 0, 123456789, time.FixedZone("", 2*3600)),
		canonical: time.Date(2026, 10, 19, 10, 0, 0, 123456000, time.UTC),
	},
	"just under the next microsecond": {
		live:      time.Date(2026, 10, 19, 10, 0, 0, 999999999, time.UTC),
		canonical: time.Date(2026, 10, 19, 10, 0, 0, 999999000, time.UTC),
	},
}

func derivedRequest(at time.Time) *MeasurementEventCreateRequest {
	return &MeasurementEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: at,
		Name:              "power_kw",
		Value:             float64Ptr(2.5),
		Derived:           true,
	}
}

func TestCanonicalEntryInstantIsUTCMicroseconds(t *testing.T) {
	for name, f := range canonicalFixtures {
		got := CanonicalEntryInstant(f.live)
		assert.Equal(t, f.canonical, got, name)
		assert.Equal(t, time.UTC, got.Location(), name)
	}
}

// Both arrival orders. Live first: the row is built from the live (ns, offset) instant, then
// the same reading is re-derived from the instant a store would return. Re-derived first is
// the reverse. Either way both copies carry one payload id, which the table's unique index
// turns into one row (the Postgres test pins that half).
func TestDerivedPayloadIdIsReproducibleFromTheStoredInstant(t *testing.T) {
	for name, f := range canonicalFixtures {
		t.Run(name, func(t *testing.T) {
			_, liveRows, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{derivedRequest(f.live)})
			require.NoError(t, err)
			require.Len(t, liveRows, 1)
			assert.Equal(t, f.canonical, liveRows[0].OccurredTime, "stored instant is truncated in Go, not by the database")

			// The instant a re-derivation (backfill) hashes is the stored one.
			_, reRows, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{derivedRequest(liveRows[0].OccurredTime)})
			require.NoError(t, err)

			assert.Equal(t, liveRows[0].PayloadId, reRows[0].PayloadId, "live-first order")
			assert.Equal(t, reRows[0].PayloadId, liveRows[0].PayloadId, "re-derived-first order")
			assert.Equal(t, f.canonical, reRows[0].OccurredTime)
		})
	}
}

// The raw branch is untouched: raw ids are never recomputed, so a redelivered raw row must
// hash exactly as it always did, and its preimage still carries the instant as sent.
func TestRawPayloadIdKeepsTheInstantAsSent(t *testing.T) {
	at := canonicalFixtures["ns +02:00"].live
	req := &MeasurementEventCreateRequest{
		Event: Event{EventId: preimageEventId}, EntryOccurredTime: at, Name: "temp", Value: float64Ptr(1),
	}
	got, err := DeriveMeasurementPayloadId(req)
	require.NoError(t, err)
	want := DerivePayloadId(preimageEventId, []byte(
		`{"Name":"temp","Value":1,"Classifier":null,"Unit":null,"DataType":null,`+
			`"Occurred":"2026-10-19T12:00:00.123456789+02:00"}`))
	assert.Equal(t, want, got)
}
