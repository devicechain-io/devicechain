// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
)

// The location wire contract, held by test rather than by prose.
//
// 🔴 Why this file exists. The location spine — decoder, protobuf, resolver,
// hypertable, GraphQL query — has been present since the beginning and had never
// once been exercised with a real coordinate. Nothing in the repository could emit
// a location event, so nothing noticed that the decoder FAILED OPEN: a body that
// put coordinates at the top of "payload" with no entries wrapper decoded
// successfully into ZERO entries, resolved, persisted nothing, and was acked to
// the device as 202. Both gateway fixtures in this package used that shape, which
// is how the next person would have learned it.
//
// The measurement of how invisible this was: deleting latitude, longitude and
// elevation from the resolver left the entire workspace suite green, four runs out
// of four. So the bar for this file is not "the decoder returns a payload" — that
// is exactly the assertion that already existed and already caught nothing. Every
// test below either reads a specific value back or names a specific body that must
// be REFUSED.

// decodeLocation drives the real decoder, exactly as a transport does.
func decodeLocation(t *testing.T, body string) (*model.UnresolvedLocationsPayload, error) {
	t.Helper()
	_, payload, err := NewJsonDecoder(map[string]string{}).Decode([]byte(body))
	if err != nil {
		return nil, err
	}
	locations, ok := payload.(*model.UnresolvedLocationsPayload)
	if !ok {
		t.Fatalf("decoded a Location event into %T, not a locations payload", payload)
	}
	return locations, nil
}

func str(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil, want %q", field, want)
	}
	if *got != want {
		t.Fatalf("%s: got %q, want %q", field, *got, want)
	}
}

// The canonical body carries every field of a fix, and every one of them arrives.
//
// The values are deliberately DISTINCT and non-round. Six fields all set to "1"
// would let a decoder that assigned accuracy to speed pass this test, which is the
// specific mistake most likely to be made when three fields are added at once.
func TestTheCanonicalLocationBodyDecodesToEveryField(t *testing.T) {
	const canonical = `{
		"device": "sp-dozer-01",
		"eventType": "Location",
		"occurredTime": "2026-08-09T12:00:00.1234567Z",
		"payload": {
			"entries": [
				{
					"latitude":  "33.74900000",
					"longitude": "-84.38800000",
					"elevation": "320.5",
					"accuracy":  "4.2",
					"speed":     "1.75",
					"heading":   "271.5",
					"occurredTime": "2026-08-09T12:00:00.1234567Z"
				}
			]
		}
	}`

	payload, err := decodeLocation(t, canonical)
	if err != nil {
		t.Fatalf("the canonical body must decode, got: %v", err)
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(payload.Entries))
	}
	entry := payload.Entries[0]
	str(t, "latitude", entry.Latitude, "33.74900000")
	str(t, "longitude", entry.Longitude, "-84.38800000")
	str(t, "elevation", entry.Elevation, "320.5")
	str(t, "accuracy", entry.Accuracy, "4.2")
	str(t, "speed", entry.Speed, "1.75")
	str(t, "heading", entry.Heading, "271.5")
	// The sample's own time arrives PARSED, at full nanosecond precision. It is the one
	// field the decoder converts rather than carries, because it is the one field
	// everything downstream orders on.
	if entry.OccurredTime == nil {
		t.Fatalf("occurredTime: got nil, want a parsed time")
	}
	wantTime, err := time.Parse(time.RFC3339Nano, "2026-08-09T12:00:00.1234567Z")
	if err != nil {
		t.Fatalf("fixture time: %v", err)
	}
	if !entry.OccurredTime.Equal(wantTime) {
		t.Fatalf("occurredTime: got %v, want %v", *entry.OccurredTime, wantTime)
	}
}

// Only latitude and longitude are required, and an absent optional stays ABSENT.
// Defaulting a missing elevation to 0 would claim the device reported sea level.
func TestAMinimalLocationDecodesAndLeavesOptionalsNil(t *testing.T) {
	payload, err := decodeLocation(t,
		`{"device":"d1","eventType":"Location","payload":{"entries":[{"latitude":"33.749","longitude":"-84.388"}]}}`)
	if err != nil {
		t.Fatalf("a minimal fix must decode, got: %v", err)
	}
	entry := payload.Entries[0]
	str(t, "latitude", entry.Latitude, "33.749")
	str(t, "longitude", entry.Longitude, "-84.388")
	for _, absent := range []struct {
		name  string
		value *string
	}{
		{"elevation", entry.Elevation},
		{"accuracy", entry.Accuracy},
		{"speed", entry.Speed},
		{"heading", entry.Heading},
	} {
		if absent.value != nil {
			t.Fatalf("%s: got %q, want nil — an unreported field must not be invented",
				absent.name, *absent.value)
		}
	}
	// occurredTime is checked apart from the table because it is a *time.Time, and it
	// matters for the same reason: absent means "the device timed only the message", and
	// inventing a value here would silently answer a question the device never answered.
	if entry.OccurredTime != nil {
		t.Fatalf("occurredTime: got %v, want nil — an unreported sample time must not be invented",
			*entry.OccurredTime)
	}
}

// Several fixes in one body keep their order and their own values. A loop that
// reuses one entry variable passes a single-entry test and fails this one.
func TestEveryEntryKeepsItsOwnValues(t *testing.T) {
	payload, err := decodeLocation(t, `{"device":"d1","eventType":"Location","payload":{"entries":[
		{"latitude":"1.5","longitude":"2.5"},
		{"latitude":"3.5","longitude":"4.5"}
	]}}`)
	if err != nil {
		t.Fatalf("a two-entry body must decode, got: %v", err)
	}
	if len(payload.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(payload.Entries))
	}
	str(t, "entries[0].latitude", payload.Entries[0].Latitude, "1.5")
	str(t, "entries[0].longitude", payload.Entries[0].Longitude, "2.5")
	str(t, "entries[1].latitude", payload.Entries[1].Latitude, "3.5")
	str(t, "entries[1].longitude", payload.Entries[1].Longitude, "4.5")
}

// 🔴 The fail-open shapes, each of which used to decode as a silent success.
//
// The three at the top are the ones that were actually in the tree as fixtures, in
// one form or another, so they are named individually rather than folded together:
// a body with no payload at all, a body whose coordinates sit directly under
// payload, and an entries list that is present but empty.
func TestLocationDecodeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "no payload object at all",
			body: `{"device":"d1","eventType":"Location"}`,
			want: "carries no entries",
		},
		{
			name: "flat: coordinates directly under payload, no entries wrapper",
			body: `{"device":"d1","eventType":"Location","payload":{"latitude":"33.749","longitude":"-84.388"}}`,
			want: "carries no entries",
		},
		{
			name: "the old fixture shape: coordinates at the envelope top level",
			body: `{"device":"d1","eventType":"Location","latitude":1,"longitude":2,"elevation":3}`,
			want: "carries no entries",
		},
		{
			name: "entries present but empty",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[]}}`,
			want: "carries no entries",
		},
		{
			name: "an entry with no coordinates at all",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[{"elevation":"1"}]}}`,
			want: `missing required field "latitude"`,
		},
		{
			// The misspelling case. json leaves an unmatched key nil, so this used to
			// decode into ONE entry holding nothing — a fail-open the entries-wrapper
			// check alone does not catch.
			name: "an entry that misspells the coordinate keys",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[{"lat":"33.749","lon":"-84.388"}]}}`,
			want: `missing required field "latitude"`,
		},
		{
			name: "latitude present, longitude missing",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[{"latitude":"33.749"}]}}`,
			want: `missing required field "longitude"`,
		},
		{
			// Coordinates are strings by contract. A bare number kills the decode, which
			// is why the canonical body quotes even the numeric fields.
			name: "coordinates sent as JSON numbers",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[{"latitude":33.749,"longitude":-84.388}]}}`,
			want: "cannot unmarshal number",
		},
		{
			name: "a non-numeric coordinate",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[{"latitude":"north","longitude":"-84.388"}]}}`,
			want: `is not a number`,
		},
		{
			// One bad entry rejects the WHOLE payload rather than being dropped: a
			// device that sends five fixes and gets 202 has been told all five landed.
			name: "one bad entry among good ones",
			body: `{"device":"d1","eventType":"Location","payload":{"entries":[
				{"latitude":"1.5","longitude":"2.5"},
				{"latitude":"999","longitude":"2.5"}
			]}}`,
			want: `entry 1 field "latitude" is out of range`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := decodeLocation(t, tc.body)
			if err == nil {
				t.Fatalf("this body must be REFUSED, but it decoded to %d entries",
					len(payload.Entries))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// Range validation, with its boundaries asserted from BOTH sides.
//
// The rejection is what makes a units bug bad DATA rather than a database error. A
// device sending degrees x 1e7 (the NMEA/LwM2M convention) overflows decimal(10,8)
// at the INSERT with SQLSTATE 22003, which is returned unwrapped and therefore
// classified as transient — so the event burns its whole redelivery budget and is
// then misfiled as a downstream failure. Refusing it here is terminal on every
// transport by construction.
//
// 🔑 The accept cases are not padding. A range check that rejected everything would
// satisfy the reject table completely, and ±90 / ±180 / heading 0 are exactly the
// legal values an off-by-one bound would throw away.
func TestLocationRangeValidation(t *testing.T) {
	body := func(fields string) string {
		return `{"device":"d1","eventType":"Location","payload":{"entries":[{` + fields + `}]}}`
	}
	const ok = `"latitude":"33.749","longitude":"-84.388"`

	t.Run("accepted at the boundary", func(t *testing.T) {
		for _, fields := range []string{
			`"latitude":"90","longitude":"180"`,
			`"latitude":"-90","longitude":"-180"`,
			`"latitude":"0","longitude":"0"`,
			ok + `,"heading":"0"`,
			ok + `,"heading":"359.9999"`, // the last bearing decimal(7,4) can hold below north
			ok + `,"speed":"0"`,
			ok + `,"accuracy":"0"`,
			ok + `,"elevation":"-431.0"`,   // the Dead Sea shore is genuinely negative
			ok + `,"elevation":"99999999"`, // the last whole metre the column holds
		} {
			if _, err := decodeLocation(t, body(fields)); err != nil {
				t.Fatalf("%s must be ACCEPTED, got: %v", fields, err)
			}
		}
	})

	t.Run("refused outside it", func(t *testing.T) {
		for _, tc := range []struct{ fields, want string }{
			{`"latitude":"90.0001","longitude":"0"`, `"latitude" is out of range`},
			{`"latitude":"-90.0001","longitude":"0"`, `"latitude" is out of range`},
			{`"latitude":"337490000","longitude":"0"`, `"latitude" is out of range`},
			{`"latitude":"0","longitude":"180.0001"`, `"longitude" is out of range`},
			{`"latitude":"0","longitude":"-180.0001"`, `"longitude" is out of range`},
			// 360 is the same bearing as 0 and the contract says [0, 360), so the
			// upper bound is EXCLUSIVE. A closed bound here would store two spellings
			// of north.
			{ok + `,"heading":"360"`, `"heading" is out of range`},
			// 🔴 The rounding attack on the exclusive bound. Both of these are BELOW
			// 360 as floats and would pass a check written at 360, then round UP to
			// exactly 360.0000 in decimal(7,4) — storing the one bearing the bound
			// exists to exclude. Measured on PostgreSQL 17.
			{ok + `,"heading":"359.99999"`, `"heading" is out of range`},
			{ok + `,"heading":"359.99995"`, `"heading" is out of range`},
			{ok + `,"heading":"-1"`, `"heading" is out of range`},
			{ok + `,"speed":"-1"`, `"speed" is out of range`},
			{ok + `,"accuracy":"-1"`, `"accuracy" is out of range`},
			{ok + `,"elevation":"1e9"`, `"elevation" is out of range`},
			// decimal(12,4) tops out at 99999999.9999, so 1e8 is the FIRST value it
			// cannot hold — a bound written at 1e8 inclusive would pass this straight
			// through to the overflow it exists to prevent.
			{ok + `,"elevation":"100000000"`, `"elevation" is out of range`},
			// And excess scale ROUNDS rather than raising, so a value just under the
			// true ceiling rounds UP over it. The bound sits a whole metre inside for
			// exactly this case.
			{ok + `,"elevation":"99999999.99999"`, `"elevation" is out of range`},
			{ok + `,"speed":"1e8"`, `"speed" is out of range`},
			{ok + `,"accuracy":"1e8"`, `"accuracy" is out of range`},
			// NaN and Inf parse as floats and fail every comparison, so a naive
			// min/max check lets them through and they reach the numeric column.
			{`"latitude":"NaN","longitude":"0"`, `"latitude" is not a finite number`},
			{`"latitude":"0","longitude":"Inf"`, `"longitude" is not a finite number`},
			{ok + `,"speed":"-Inf"`, `"speed" is not a finite number`},
		} {
			_, err := decodeLocation(t, body(tc.fields))
			if err == nil {
				t.Fatalf("%s must be REFUSED, but it decoded", tc.fields)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error %q does not mention %q", tc.fields, err.Error(), tc.want)
			}
		}
	})
}

// The same fail-open shape existed on measurements and alerts, and it is closed
// here for the same reason.
//
// 🔑 This test is the answer to "which OTHER instances of this defect exist".
// Location was found by investigation because nothing had ever emitted one;
// measurement is the path every SDK, simulator and adapter actually takes, and its
// flat form was sitting in this package's own fixtures — in the test that calls
// itself "a well-formed device Measurement event", and in the one named
// DecodeSuccess. Both asserted success for a body that stored nothing. Fixing only
// the instance that was investigated would have left the worse one in place.
func TestMeasurementAndAlertDecodeAlsoFailClosed(t *testing.T) {
	jd := NewJsonDecoder(map[string]string{})
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "flat measurement: no entries wrapper",
			body: `{"device":"d1","eventType":"Measurement","payload":{"measurements":{"temp":"21.5"}}}`,
			want: "measurement payload carries no entries",
		},
		{
			name: "measurement entries present but empty",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[]}}`,
			want: "measurement payload carries no entries",
		},
		{
			name: "flat alert: no entries wrapper",
			body: `{"device":"d1","eventType":"Alert","payload":{"type":"a","level":1,"message":"m","source":"s"}}`,
			want: "alert payload carries no entries",
		},
		{
			name: "alert entries present but empty",
			body: `{"device":"d1","eventType":"Alert","payload":{"entries":[]}}`,
			want: "alert payload carries no entries",
		},
		// 🔴 One level in. The entries wrapper alone is only half the check: an entry
		// that is present but carries nothing is the same silent success, and for
		// alerts it is worse than silence — it STORES a row with an empty type,
		// message and source at level 0, which nothing can route and nobody can find.
		{
			name: "measurement entry carrying no measurements",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[{}]}}`,
			want: "measurement entry 0 carries no measurements",
		},
		{
			name: "measurement entry with an empty measurements map",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[{"measurements":{}}]}}`,
			want: "measurement entry 0 carries no measurements",
		},
		{
			name: "one empty measurement entry among good ones",
			body: `{"device":"d1","eventType":"Measurement","payload":{"entries":[` +
				`{"measurements":{"temp":"21.5"}},{}]}}`,
			want: "measurement entry 1 carries no measurements",
		},
		{
			name: "alert entry with no type",
			body: `{"device":"d1","eventType":"Alert","payload":{"entries":[{"message":"m","source":"s"}]}}`,
			want: "alert entry 0 has no type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := jd.Decode([]byte(tc.body)); err == nil {
				t.Fatal("this body must be REFUSED, but it decoded successfully")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// The counterweight: the canonical nested forms still decode untouched. Rejecting
// an empty entries list is only safe while well-formed input keeps passing.
func TestTheCanonicalMeasurementAndAlertBodiesStillDecode(t *testing.T) {
	jd := NewJsonDecoder(map[string]string{})
	for _, body := range []string{
		`{"device":"d1","eventType":"Measurement","payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`,
		`{"device":"d1","eventType":"Alert","payload":{"entries":[{"type":"a","level":1,"message":"m","source":"s"}]}}`,
		// Type is the only required alert field. A blank message is unhelpful, not
		// corrupt, and refusing it would be a stricter contract than anything asked
		// for — this case is what keeps the check from quietly becoming one.
		`{"device":"d1","eventType":"Alert","payload":{"entries":[{"type":"a"}]}}`,
	} {
		if _, _, err := jd.Decode([]byte(body)); err != nil {
			t.Fatalf("%s must decode, got: %v", body, err)
		}
	}
}
