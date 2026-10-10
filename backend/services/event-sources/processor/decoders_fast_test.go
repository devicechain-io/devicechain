// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// devicepulseBody is the body the GKE benchmark's simulator sends (see httpBenchBody), at a
// fixed time inside the equivalence tests' age window.
const devicepulseBody = `{"device":"dp-00007","occurredTime":"2026-10-01T11:59:00.123Z","eventType":"Measurement",` +
	`"payload":{"entries":[{"measurements":{"cpu":"42.17"},"occurredTime":"2026-10-01T11:59:00.123Z"}]},` +
	`"credentialType":"ACCESS_TOKEN","credentialId":"cred-dp-00007-0123456789abcdef"}`

// fastCases is the equivalence table for the fast decode. Every case is held to the reference
// by requireSameDecodePaths; fast is whether the fast decode is expected to answer it itself.
// The rows that decline are as much the point as the ones taken: each is an input the fast
// decode cannot state the meaning of without encoding/json, and must leave to it.
func fastCases() []equivCase {
	meas := func(readings string) string {
		return envelope("Measurement", `{"entries":[{"measurements":{`+readings+`}}]}`)
	}
	entry := func(e string) string { return envelope("Measurement", `{"entries":[`+e+`]}`) }
	cases := []equivCase{
		// Taken.
		{"devicepulse", devicepulseBody, true},
		{"one reading", meas(`"temperature":"21.5"`), true},
		{"all envelope fields", `{"altId":"x","device":"d","relationship":"r","occurredTime":"2026-10-01T11:59:00Z","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]},"credentialType":"t","credentialId":"i","credentialSecret":"s"}`, true},
		{"payload first", `{"payload":{"entries":[{"measurements":{"a":"1"}}]},"eventType":"Measurement","device":"d"}`, true},
		{"no device", `{"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, true},
		{"empty strings", `{"device":"","altId":"","eventType":"Measurement","payload":{"entries":[{"measurements":{"":""}}]}}`, true},
		{"whitespace everywhere", " \t\r\n{ \"device\" : \"d\" ,\n\"eventType\"\t:\r\"Measurement\" , \"payload\" : { \"entries\" : [ { \"measurements\" : { \"a\" : \"1\" } , \"occurredTime\" : \"2026-10-01T11:00:00Z\" } ] } }\n\t ", true},
		{"several entries", entry(`{"measurements":{"a":"1"}},{"occurredTime":"2026-10-01T11:00:00Z","measurements":{"a":"2","b":"3"}}`), true},
		{"readings differing only in case", meas(`"Temp":"1","temp":"2","TEMP":"3"`), true},
		{"printable ascii in values", meas(`"a":" !#$%&'()*+,-./:;<=>?@[]^_{|}~` + "\x7f" + `"`), true},
		{"HTML-escaped bytes in a name", meas(`"<a&b>":"1"`), true},
		{"256 readings", envelope("Measurement", bigObject(256)), true},
		// Numbers inside a measurement are strings, so they are taken verbatim: the reference does
		// not parse them either.
		{"number-shaped strings", meas(`"exp":"1e400","neg":"-0","nan":"NaN","inf":"-Inf","huge":"18446744073709551616","tiny":"1e-400","hex":"0x1F"`), true},

		// Numbers as JSON numbers: declined everywhere, whatever the reference makes of them.
		{"number reading", meas(`"a":21.5`), false},
		{"negative zero reading", meas(`"a":-0`), false},
		{"exponent reading", meas(`"a":1e3`), false},
		{"huge exponent reading", meas(`"a":1e400`), false},
		{"huge integer reading", meas(`"a":18446744073709551616`), false},
		{"NaN is not JSON", meas(`"a":NaN`), false},
		{"Infinity is not JSON", meas(`"a":Infinity`), false},
		{"number in an unknown envelope field", `{"device":"d","eventType":"Measurement","x":1e400,"payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"number as device", `{"device":5,"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"bool reading", meas(`"a":true`), false},

		// Escapes: declined in names and values, whether or not they decode to plain ASCII.
		{"unicode escape in a value", meas(`"a":"\u0031"`), false},
		{"unicode escape in a name", meas(`"\u0061":"1"`), false},
		{"unicode escape in an envelope key", `{"devic\u0065":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"escaped event type", `{"device":"d","eventType":"Measuremen\u0074","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"surrogate escape", meas(`"a":"\ud83d\ude00"`), false},
		{"lone surrogate escape", meas(`"a":"\ud800"`), false},
		{"escaped quote", meas(`"a":"say \"hi\""`), false},
		{"escaped time", entry(`{"occurredTime":"2026-10-01T11:00:00\u005a","measurements":{"a":"1"}}`), false},
		{"utf-8 value", meas(`"a":"21.5°C"`), false},
		{"invalid utf-8 value", meas("\"a\":\"x\xffy\""), false},
		{"control byte in a value", meas("\"a\":\"x\ty\""), false},

		// Duplicate and re-spelled keys.
		{"duplicate reading", meas(`"a":"1","a":"2"`), false},
		{"duplicate reading, case differs and exact repeat", meas(`"a":"1","A":"2","a":"3"`), false},
		{"duplicate device", `{"device":"d1","device":"d2","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"duplicate event type", `{"device":"d","eventType":"Measurement","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"duplicate payload", `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]},"payload":{"entries":[{"measurements":{"b":"2"}}]}}`, false},
		{"duplicate entries", envelope("Measurement", `{"entries":[{"measurements":{"a":"1"}}],"entries":[{"measurements":{"b":"2"}}]}`), false},
		{"duplicate measurements", entry(`{"measurements":{"a":"1"},"measurements":{"b":"2"}}`), false},
		{"duplicate entry time", entry(`{"occurredTime":"2026-10-01T11:00:00Z","measurements":{"a":"1"},"occurredTime":"2026-10-01T11:01:00Z"}`), false},
		{"respelled envelope key", `{"Device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"respelled payload key", `{"device":"d","eventType":"Measurement","Payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"respelled entries key", envelope("Measurement", `{"Entries":[{"measurements":{"a":"1"}}]}`), false},
		{"respelled measurements key", entry(`{"Measurements":{"a":"1"}}`), false},
		{"respelled entry time key", entry(`{"OccurredTime":"2026-10-01T11:00:00Z","measurements":{"a":"1"}}`), false},

		// Unknown fields, at each level.
		{"unknown envelope field", `{"device":"d","eventType":"Measurement","extra":"x","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"unknown payload field", envelope("Measurement", `{"entries":[{"measurements":{"a":"1"}}],"x":"y"}`), false},
		{"unknown payload field first", envelope("Measurement", `{"x":"y","entries":[{"measurements":{"a":"1"}}]}`), false},
		{"unknown entry field", entry(`{"measurements":{"a":"1"},"x":"y"}`), false},
		{"nested reading", meas(`"a":{"b":"1"}`), false},
		{"array reading", meas(`"a":["1"]`), false},

		// Nulls.
		{"null reading", meas(`"a":null`), false},
		{"null device", `{"device":null,"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"null alt id", `{"altId":null,"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"null payload", envelope("Measurement", `null`), false},
		{"null entries", envelope("Measurement", `{"entries":null}`), false},
		{"null entry", entry(`null`), false},
		{"null measurements", entry(`{"measurements":null}`), false},
		{"null entry time", entry(`{"occurredTime":null,"measurements":{"a":"1"}}`), false},
		{"null envelope time", `{"device":"d","occurredTime":null,"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},

		// Empty and missing structure: all reference errors.
		{"empty envelope", `{}`, false},
		{"no payload", `{"device":"d","eventType":"Measurement"}`, false},
		{"no event type", `{"device":"d","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"empty payload", envelope("Measurement", `{}`), false},
		{"empty entries", envelope("Measurement", `{"entries":[]}`), false},
		{"empty entry", entry(`{}`), false},
		{"entry with only a time", entry(`{"occurredTime":"2026-10-01T11:00:00Z"}`), false},
		{"empty measurements", entry(`{"measurements":{}}`), false},
		{"other event type", envelope("Alert", `{"entries":[{"type":"t","level":1}]}`), false},
		{"lowercase event type", envelope("measurement", `{"entries":[{"measurements":{"a":"1"}}]}`), false},
		{"257 readings", envelope("Measurement", bigObject(257)), false},

		// Malformed JSON.
		{"trailing garbage", meas(`"a":"1"`) + `x`, false},
		{"trailing comma in readings", meas(`"a":"1",`), false},
		{"trailing comma in envelope", `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]},}`, false},
		{"missing colon", meas(`"a" "1"`), false},
		{"unterminated string", meas(`"a":"1`), false},
		{"truncated", meas(`"a":"1"`)[:40], false},
		{"two documents", meas(`"a":"1"`) + meas(`"a":"1"`), false},
		{"form feed is not JSON whitespace", "\f" + meas(`"a":"1"`), false},
		{"byte order mark", "\xef\xbb\xbf" + meas(`"a":"1"`), false},

		// Times: the envelope's through time.Parse, an entry's through the typed decode's parser.
		{"entry time with fraction", entry(`{"occurredTime":"2026-10-01T11:00:00.123456789Z","measurements":{"a":"1"}}`), true},
		{"entry time with offset", entry(`{"occurredTime":"2026-10-01T13:00:00+02:00","measurements":{"a":"1"}}`), true},
		{"entry time lowercase t and z", entry(`{"occurredTime":"2026-10-01t11:00:00z","measurements":{"a":"1"}}`), false},
		{"entry time hour 24", entry(`{"occurredTime":"2026-10-01T24:00:00Z","measurements":{"a":"1"}}`), false},
		// Both parsers accept these two, so both paths do.
		{"entry time offset hour 24", entry(`{"occurredTime":"2026-10-01T11:00:00+24:00","measurements":{"a":"1"}}`), true},
		{"entry time trailing fraction dot", entry(`{"occurredTime":"2026-10-01T11:00:00.Z","measurements":{"a":"1"}}`), false},
		{"entry time comma fraction", entry(`{"occurredTime":"2026-10-01T11:00:00,5Z","measurements":{"a":"1"}}`), true},
		{"entry time no zone", entry(`{"occurredTime":"2026-10-01T11:00:00","measurements":{"a":"1"}}`), false},
		{"entry time date only", entry(`{"occurredTime":"2026-10-01","measurements":{"a":"1"}}`), false},
		{"entry time epoch number", entry(`{"occurredTime":1759320000,"measurements":{"a":"1"}}`), false},
		{"entry time too old", entry(`{"occurredTime":"2019-05-05T00:00:00Z","measurements":{"a":"1"}}`), false},
		{"entry time in the future", entry(`{"occurredTime":"2030-01-01T00:00:00Z","measurements":{"a":"1"}}`), true},
		{"entry time zero instant", entry(`{"occurredTime":"0001-01-01T00:00:00Z","measurements":{"a":"1"}}`), false},
		{"entry time zero instant in another zone", entry(`{"occurredTime":"0001-01-01T01:00:00+01:00","measurements":{"a":"1"}}`), false},
		{"second entry time zero instant", entry(`{"measurements":{"a":"1"}},{"occurredTime":"0001-01-01T00:00:00Z","measurements":{"a":"1"}}`), false},
		{"envelope time with fraction", `{"device":"d","occurredTime":"2026-10-01T11:59:00.5Z","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, true},
		{"envelope time lowercase t", `{"device":"d","occurredTime":"2026-10-01t11:59:00Z","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time not rfc3339", `{"device":"d","occurredTime":"yesterday","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time zero instant", `{"device":"d","occurredTime":"0001-01-01T00:00:00Z","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time too old", `{"device":"d","occurredTime":"2020-01-01T00:00:00Z","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time too old, entry time recent", `{"device":"d","occurredTime":"2020-01-01T00:00:00Z","eventType":"Measurement","payload":{"entries":[{"occurredTime":"2026-10-01T11:00:00Z","measurements":{"a":"1"}}]}}`, false},
		{"envelope time number", `{"device":"d","occurredTime":1759320000,"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
	}
	return cases
}

func TestDecodeFastMatchesReference(t *testing.T) {
	for _, tc := range fastCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, fast := requireSameDecodePaths(t, []byte(tc.payload))
			require.Equal(t, tc.fast, fast, "fast decode taken: want %v", tc.fast)
		})
	}
}

// TestDecodeFastIsTakenForARealisticEvent guards the premise: the table above is vacuous if the
// fast decode quietly declines what devices actually send.
func TestDecodeFastIsTakenForARealisticEvent(t *testing.T) {
	for name, body := range map[string][]byte{
		"devicepulse":  []byte(devicepulseBody),
		"metrics=1":    measurementsBody(1),
		"metrics=10":   measurementsBody(10),
		"metrics=256":  []byte(envelope("Measurement", bigObject(256))),
		"pretty-print": indented(t, measurementsBody(3)),
	} {
		_, fast := requireSameDecodePaths(t, body)
		require.True(t, fast, name)
	}
}

func indented(t *testing.T, body []byte) []byte {
	var out bytes.Buffer
	require.NoError(t, json.Indent(&out, body, "", "	"))
	return out.Bytes()
}

// TestDecodeAllocationsOnTheCommonShape is what the fast decode is FOR, measured where it can
// fail: the single-pass decode spends 31 allocations on a one-reading event.
func TestDecodeAllocationsOnTheCommonShape(t *testing.T) {
	jd := NewJsonDecoder(nil)
	body := measurementsBody(1)
	allocs := testing.AllocsPerRun(200, func() {
		if _, _, err := jd.Decode(body, equivReceivedAt); err != nil {
			t.Fatal(err)
		}
	})
	require.LessOrEqual(t, allocs, float64(maxCommonShapeAllocs), "allocations per one-reading Decode")
}

// maxCommonShapeAllocs is the ceiling TestDecodeAllocationsOnTheCommonShape holds Decode to: the
// fast decode spends 10 on this event (measured with go1.26), plus slack for a toolchain whose
// escape analysis differs.
const maxCommonShapeAllocs = 12

// FuzzDecodeFast is FuzzDecodeSinglePass seeded from the fast decode's own table, so the fuzzer
// starts inside the grammar the fast decode takes and mutates out of it.
func FuzzDecodeFast(f *testing.F) {
	for _, tc := range fastCases() {
		if len(tc.payload) < 4000 {
			f.Add([]byte(tc.payload))
		}
	}
	f.Add(measurementsBody(10))
	f.Fuzz(func(t *testing.T, payload []byte) {
		requireSameDecodePaths(t, payload)
	})
}
