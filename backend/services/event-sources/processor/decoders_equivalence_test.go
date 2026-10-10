// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/require"
)

// referenceDecode is Decode as it was before the payload was decoded once: the envelope into a
// map, then a re-marshal of that map per kind. It is a copy, not a call into decodeReference,
// so the equivalence tests keep a fixed reference even if the production fallback is edited.
// Build*Payload still marshal the map and run the shared validation, which is the old path.
func referenceDecode(jd *JsonDecoder, payload []byte, receivedAt time.Time) (*model.UnresolvedEvent, interface{}, error) {
	jevent, err := jd.ParseEvent(payload)
	if err != nil {
		return nil, nil, err
	}
	event, err := jd.AssembleEvent(jevent, receivedAt)
	if err != nil {
		return nil, nil, err
	}
	var built interface{}
	switch event.EventType {
	case model.NewRelationship:
		built, err = jd.BuildNewRelationshipPayload(jevent)
	case model.Location:
		built, err = jd.BuildLocationsPayload(jevent)
	case model.Measurement:
		built, err = jd.BuildMeasurementsPayload(jevent)
	case model.Alert:
		built, err = jd.BuildAlertsPayload(jevent)
	case model.StateChange:
		return nil, nil, fmt.Errorf("state-change (presence) events are platform-produced and not accepted from device ingest")
	default:
		return nil, nil, fmt.Errorf("unhandled event type: %s", jevent.EventType)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := model.CheckReadingCount(built); err != nil {
		return nil, nil, err
	}
	if err := model.CheckEventAge(event.OccurredTime, event.ProcessedTime, built); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidEventTime, err)
	}
	return event, built, nil
}

var equivReceivedAt = time.Date(2026, 10, 1, 12, 0, 1, 0, time.UTC)

// requireSameDecode runs payload through the reference and the production Decode and requires
// identical events, identical payloads and identical errors (text and ErrInvalidEventTime
// classification). It returns whether the single-pass decode produced the result itself.
func requireSameDecode(t *testing.T, payload []byte) (fast bool) {
	t.Helper()
	jd := NewJsonDecoder(nil)
	wantEvent, wantBuilt, wantErr := referenceDecode(jd, payload, equivReceivedAt)
	gotEvent, gotBuilt, gotErr := jd.Decode(payload, equivReceivedAt)
	if wantErr != nil {
		require.Error(t, gotErr)
		require.Equal(t, wantErr.Error(), gotErr.Error())
		require.Equal(t, errors.Is(wantErr, ErrInvalidEventTime), errors.Is(gotErr, ErrInvalidEventTime))
		require.Nil(t, gotEvent)
		require.Nil(t, gotBuilt)
	} else {
		require.NoError(t, gotErr)
		require.Equal(t, wantEvent, gotEvent)
		require.Equal(t, wantBuilt, gotBuilt)
	}
	_, _, fast = jd.decodeOnce(payload, equivReceivedAt)
	if fast {
		require.NoError(t, wantErr, "the single-pass decode accepted an input the reference rejects")
	}
	return fast
}

func envelope(eventType, payload string) string {
	return `{"device":"dev-1","eventType":"` + eventType + `","payload":` + payload + `}`
}

// bigObject builds {"entries":[{"measurements":{"m0000":"1",...}}]} with n readings.
func bigObject(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"m%04d":"%d"`, i, i)
	}
	return `{"entries":[{"measurements":{` + sb.String() + `}}]}`
}

type equivCase struct {
	name    string
	payload string
	// fast is whether the single-pass decode is expected to produce the result itself. False for
	// every rejection (the reference owns the error) and for input it declines to take.
	fast bool
}

func equivCases() []equivCase {
	const m = "Measurement"
	cases := []equivCase{
		// Accepted, and taken by the single-pass decode.
		{"measurement one reading", envelope(m, `{"entries":[{"measurements":{"temperature":"21.5"}}]}`), true},
		{"measurement several entries and readings", envelope(m, `{"entries":[{"measurements":{"a":"1","b":"2.50","c":"-0"}},{"measurements":{"a":"3"}}]}`), true},
		{"measurement entry and envelope times", `{"device":"d","eventType":"Measurement","occurredTime":"2026-10-01T11:59:00Z","payload":{"entries":[{"occurredTime":"2026-10-01T11:58:00+02:00","measurements":{"a":"1"}}]}}`, true},
		{"measurement envelope extras", `{"altId":"x","device":"d","relationship":"r","credentialType":"t","credentialId":"i","credentialSecret":"s","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, true},
		{"measurement unknown fields everywhere", `{"device":"d","eventType":"Measurement","extra":{"x":[1,2,{"y":null}]},"payload":{"unknown":[1,2500.5,true,null],"entries":[{"junk":{"k":1},"measurements":{"a":"1"}}]}}`, true},
		{"measurement null value and empty string", envelope(m, `{"entries":[{"measurements":{"a":null,"b":""}}]}`), true},
		{"measurement big-integer strings", envelope(m, `{"entries":[{"measurements":{"a":"18446744073709551616","b":"9007199254740993","c":"1e400","d":"0.1000000000000000055511151231257827"}}]}`), true},
		{"measurement lowercase-distinct keys", envelope(m, `{"entries":[{"measurements":{"Temp":"1","temp":"2"}}]}`), false},
		{"measurement value invalid utf-8", "{\"device\":\"d\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"a\":\"x\xffy\"}}]}}", true},
		{"measurement value escapes", envelope(m, `{"entries":[{"measurements":{"a":"q\"u\\o\u00e9\ud800\n<>&"}}]}`), true},
		{"measurement whitespace", " \n" + envelope(m, `{ "entries" : [ { "measurements" : { "a" : "1" } } ] }`) + " \n", true},
		{"measurement 256 readings", envelope(m, bigObject(256)), true},
		{"measurement 2000 readings is over the limit", envelope(m, bigObject(2000)), false},
		{"measurement deep unknown nesting", envelope(m, `{"deep":`+strings.Repeat(`[`, 200)+strings.Repeat(`]`, 200)+`,"entries":[{"measurements":{"a":"1"}}]}`), true},

		{"location full", envelope("Location", `{"entries":[{"latitude":"33.749","longitude":"-84.388","elevation":"10","accuracy":"3.5","speed":"0","heading":"359.9999","occurredTime":"2026-10-01T11:00:00Z"}]}`), true},
		{"location minimal", envelope("Location", `{"entries":[{"latitude":"0","longitude":"0"}]}`), true},
		{"location bare number", envelope("Location", `{"entries":[{"latitude":33.749,"longitude":"-84.388"}]}`), false},
		{"location missing longitude", envelope("Location", `{"entries":[{"latitude":"1"}]}`), false},
		{"location out of range", envelope("Location", `{"entries":[{"latitude":"91","longitude":"0"}]}`), false},
		{"location heading rounds to 360", envelope("Location", `{"entries":[{"latitude":"1","longitude":"1","heading":"359.99995"}]}`), false},
		{"location NaN", envelope("Location", `{"entries":[{"latitude":"NaN","longitude":"1"}]}`), false},
		{"location not a number", envelope("Location", `{"entries":[{"latitude":"north","longitude":"1"}]}`), false},
		{"location no entries", envelope("Location", `{"latitude":"1","longitude":"1"}`), false},

		{"alert full", envelope("Alert", `{"entries":[{"type":"overheat","level":3,"message":"hot","source":"s1","occurredTime":"2026-10-01T11:00:00Z"}]}`), true},
		{"alert max int32 level", envelope("Alert", `{"entries":[{"type":"t","level":2147483647}]}`), true},
		{"alert level over int32", envelope("Alert", `{"entries":[{"type":"t","level":2147483648}]}`), false},
		{"alert level over uint32", envelope("Alert", `{"entries":[{"type":"t","level":4294967296}]}`), false},
		{"alert level negative", envelope("Alert", `{"entries":[{"type":"t","level":-1}]}`), false},
		{"alert level 1.0", envelope("Alert", `{"entries":[{"type":"t","level":1.0}]}`), false},
		{"alert level 1e2", envelope("Alert", `{"entries":[{"type":"t","level":1e2}]}`), false},
		{"alert level 1.5", envelope("Alert", `{"entries":[{"type":"t","level":1.5}]}`), false},
		{"alert level as string", envelope("Alert", `{"entries":[{"type":"t","level":"3"}]}`), false},
		{"alert level huge float", envelope("Alert", `{"entries":[{"type":"t","level":5000000000000000000000}]}`), false},
		{"alert no type", envelope("Alert", `{"entries":[{"level":1}]}`), false},
		{"alert type wrong kind", envelope("Alert", `{"entries":[{"type":5}]}`), false},

		{"relationship", envelope("NewRelationship", `{"relationshipType":"contains","targetType":"area","target":"a1"}`), true},
		{"relationship numeric and nested values", envelope("NewRelationship", `{"relationshipType":12345678901234567890,"targetType":1.0,"target":{"a":[1,2]}}`), true},
		{"relationship empty", envelope("NewRelationship", `{}`), true},
		{"relationship null payload", envelope("NewRelationship", `null`), true},
		{"relationship absent payload", `{"device":"d","eventType":"NewRelationship"}`, true},
		{"relationship array payload", envelope("NewRelationship", `[]`), false},

		// Rejections of the envelope and the payload shape.
		{"empty body", ``, false},
		{"not json", `hello`, false},
		{"trailing garbage", envelope(m, `{"entries":[{"measurements":{"a":"1"}}]}`) + `x`, false},
		{"truncated", envelope(m, `{"entries":[{"measurements":{"a":"1"}`), false},
		{"empty object", `{}`, false},
		{"unknown event type", envelope("Nope", `{}`), false},
		{"state change forbidden", envelope("StateChange", `{"state":"CONNECTED"}`), false},
		{"command invocation unhandled", envelope("CommandInvocation", `{}`), false},
		{"event type wrong kind", `{"device":"d","eventType":5,"payload":{}}`, false},
		{"device wrong kind", `{"device":5,"eventType":"Measurement","payload":{}}`, false},
		{"null payload", envelope(m, `null`), false},
		{"absent payload", `{"device":"d","eventType":"Measurement"}`, false},
		{"empty payload object", envelope(m, `{}`), false},
		{"array payload", envelope(m, `[]`), false},
		{"string payload", envelope(m, `"x"`), false},
		{"number payload", envelope(m, `5`), false},
		{"entries null", envelope(m, `{"entries":null}`), false},
		{"entries empty", envelope(m, `{"entries":[]}`), false},
		{"entries not an array", envelope(m, `{"entries":{"a":1}}`), false},
		{"entry empty", envelope(m, `{"entries":[{}]}`), false},
		{"entry null", envelope(m, `{"entries":[null]}`), false},
		{"entry measurements empty", envelope(m, `{"entries":[{"measurements":{}}]}`), false},
		{"measurement value is a number", envelope(m, `{"entries":[{"measurements":{"a":21.5}}]}`), false},
		{"measurement value is a bool", envelope(m, `{"entries":[{"measurements":{"a":true}}]}`), false},
		{"measurement two bad values", envelope(m, `{"entries":[{"measurements":{"b":2,"a":1}}]}`), false},
		{"two entries one bad", envelope(m, `{"entries":[{"measurements":{"a":"1"}},{"measurements":{"a":1}}]}`), false},

		// Times.
		{"envelope time not rfc3339", `{"device":"d","eventType":"Measurement","occurredTime":"yesterday","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time zero instant", `{"device":"d","eventType":"Measurement","occurredTime":"0001-01-01T00:00:00Z","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time too old", `{"device":"d","eventType":"Measurement","occurredTime":"2020-01-01T00:00:00Z","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"envelope time number", `{"device":"d","eventType":"Measurement","occurredTime":1759320000,"payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"entry time not rfc3339", envelope(m, `{"entries":[{"occurredTime":"2006","measurements":{"a":"1"}}]}`), false},
		{"entry time zero instant", envelope(m, `{"entries":[{"occurredTime":"0001-01-01T00:00:00Z","measurements":{"a":"1"}}]}`), false},
		{"entry time too old", envelope(m, `{"entries":[{"occurredTime":"2019-05-05T00:00:00Z","measurements":{"a":"1"}}]}`), false},
		{"entry time number", envelope(m, `{"entries":[{"occurredTime":1759320000,"measurements":{"a":"1"}}]}`), false},
		{"entry time null", envelope(m, `{"entries":[{"occurredTime":null,"measurements":{"a":"1"}}]}`), true},
		{"entry time escaped but valid", envelope(m, `{"entries":[{"occurredTime":"2026-10-01T11:00:00\u005a","measurements":{"a":"1"}}]}`), false},
		{"entry time in the future", envelope(m, `{"entries":[{"occurredTime":"2030-01-01T00:00:00Z","measurements":{"a":"1"}}]}`), true},
		// The single-pass decode skips the entry-time probe and refuses the zero instant on the
		// decoded entries instead, for every entry-carrying kind and at any entry.
		{"second entry time zero instant", envelope(m, `{"entries":[{"measurements":{"a":"1"}},{"occurredTime":"0001-01-01T00:00:00Z","measurements":{"a":"1"}}]}`), false},
		{"entry time zero instant in another zone", envelope(m, `{"entries":[{"occurredTime":"0001-01-01T01:00:00+01:00","measurements":{"a":"1"}}]}`), false},
		{"location entry time zero instant", envelope("Location", `{"entries":[{"latitude":"1","longitude":"2","occurredTime":"0001-01-01T00:00:00Z"}]}`), false},
		{"alert entry time zero instant", envelope("Alert", `{"entries":[{"type":"t","level":1,"occurredTime":"0001-01-01T00:00:00Z"}]}`), false},
		{"location entry time number", envelope("Location", `{"entries":[{"latitude":"1","longitude":"2","occurredTime":5}]}`), false},
		{"alert entry time not rfc3339", envelope("Alert", `{"entries":[{"type":"t","level":1,"occurredTime":"noon"}]}`), false},
		{"alert entry time valid", envelope("Alert", `{"entries":[{"type":"t","level":1,"occurredTime":"2026-10-01T11:00:00Z"}]}`), true},

		// Duplicate and re-spelled keys: a direct decode and the map round trip disagree, so
		// the single-pass decode must decline every one of these.
		{"duplicate payload key", `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]},"payload":{"entries":[{"measurements":{"b":"2"}}]}}`, false},
		{"duplicate payload key merges maps", `{"device":"d","eventType":"Measurement","payload":{"x":1},"payload":{"entries":[{"measurements":{"b":"2"}}]}}`, false},
		{"duplicate entries key", envelope(m, `{"entries":[{"measurements":{"a":"1"}}],"entries":[{"measurements":{"b":"2"}}]}`), false},
		{"duplicate entries key, second is longer", envelope(m, `{"entries":[{"measurements":{"a":"1"}}],"entries":[{"measurements":{"b":"2"}},{"measurements":{"c":"3"}}]}`), false},
		{"duplicate measurements key", envelope(m, `{"entries":[{"measurements":{"a":"1"},"measurements":{"b":"2"}}]}`), false},
		{"duplicate reading name", envelope(m, `{"entries":[{"measurements":{"a":"1","a":"2"}}]}`), false},
		{"duplicate envelope device", `{"device":"d1","device":"d2","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"duplicate alert field", envelope("Alert", `{"entries":[{"type":"a","type":"b","level":1}]}`), false},
		{"duplicate inside unknown field", envelope(m, `{"junk":{"k":1,"k":2},"entries":[{"measurements":{"a":"1"}}]}`), false},
		{"entries respelled", envelope(m, `{"Entries":[{"measurements":{"a":"1"}}],"entries":[{"measurements":{"b":"2"}}]}`), false},
		{"entries respelled, first fails", envelope(m, `{"ENTRIES":5,"entries":[{"measurements":{"b":"2"}}]}`), false},
		{"entry field respelled", envelope("Alert", `{"entries":[{"Type":"a","type":"b","level":1}]}`), false},
		{"envelope field respelled", `{"Device":"d1","device":"d2","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"payload key respelled", `{"device":"d","eventType":"Measurement","Payload":{"entries":[{"measurements":{"a":"9"}}]},"payload":{"entries":[{"measurements":{"b":"2"}}]}}`, false},
		{"mixed-case struct keys without a collision", `{"Device":"d","EventType":"Measurement","Payload":{"Entries":[{"Measurements":{"a":"1"}}]}}`, true},
		{"many keys with a duplicate", envelope(m, strings.Replace(bigObject(40), `"m0039":"39"`, `"m0000":"39"`, 1)), false},
		{"many keys with a respelled duplicate", envelope(m, strings.Replace(bigObject(40), `"m0039":"39"`, `"M0000":"39"`, 1)), false},
		// A number that overflows float64 inside a field the typed structs ignore: the reference
		// decodes every value into interface{} and rejects the whole document, so the single-pass
		// decode must decline rather than skip the field.
		{"overflow in unknown payload field", `{"device":"dev-1","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}],"x":1e400}}`, false},
		{"overflow in unknown entry field", envelope(m, `{"entries":[{"measurements":{"a":"1"},"x":-1e400}]}`), false},
		{"overflow in unknown location field", envelope("Location", `{"entries":[{"latitude":"1","longitude":"2","x":[1e999]}]}`), false},
		{"overflow in unknown alert field", envelope("Alert", `{"entries":[{"type":"t","level":1,"x":{"y":1e400}}]}`), false},
		{"overflow in unknown envelope field", `{"device":"d","eventType":"Measurement","x":1E+400,"payload":{"entries":[{"measurements":{"a":"1"}}]}}`, false},
		{"309-digit integer in unknown field", envelope(m, `{"x":1`+strings.Repeat("0", 308)+`,"entries":[{"measurements":{"a":"1"}}]}`), false},
		{"308-digit integer in unknown field", envelope(m, `{"x":1`+strings.Repeat("0", 307)+`,"entries":[{"measurements":{"a":"1"}}]}`), true},
		{"underflow in unknown field", envelope(m, `{"x":1e-400,"entries":[{"measurements":{"a":"1"}}]}`), true},
		{"exponent in alert level", envelope("Alert", `{"entries":[{"type":"t","level":1e0}]}`), false},
		{"benign exponent in unknown field", envelope(m, `{"x":2.5e3,"entries":[{"measurements":{"a":"1"}}]}`), false},
		{"many distinct keys", envelope(m, bigObject(40)), true},

		// Keys the scan will not reason about.
		{"escaped key", envelope(m, `{"entries":[{"measurements":{"te\u006dp":"1"}}]}`), false},
		{"escaped key duplicating a plain one", envelope(m, `{"entries":[{"measurements":{"temp":"1","te\u006dp":"2"}}]}`), false},
		{"non-ascii key", envelope(m, `{"entries":[{"measurements":{"température":"1"}}]}`), false},
		{"invalid utf-8 keys that repair to one", "{\"device\":\"d\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"a\xff\":\"1\",\"a\xfe\":\"2\"}}]}}", false},
	}
	return cases
}

func TestDecodeSinglePassMatchesReference(t *testing.T) {
	for _, tc := range equivCases() {
		t.Run(tc.name, func(t *testing.T) {
			fast := requireSameDecode(t, []byte(tc.payload))
			require.Equal(t, tc.fast, fast, "single-pass decode taken: want %v", tc.fast)
		})
	}
}

// TestDecodeSinglePassIsTakenForARealisticEvent guards the premise of the whole change: the
// equivalence table above is vacuous if the single-pass decode silently declines everything.
func TestDecodeSinglePassIsTakenForARealisticEvent(t *testing.T) {
	for _, n := range []int{1, 10} {
		require.True(t, requireSameDecode(t, measurementsBody(n)), "metrics=%d", n)
	}
}

// TestDecodeSinglePassMatchesReferenceUnderMutation damages valid events byte by byte and
// requires the two paths to agree on each. The seed is fixed so a failure reproduces.
func TestDecodeSinglePassMatchesReferenceUnderMutation(t *testing.T) {
	var seeds [][]byte
	for _, tc := range equivCases() {
		if len(tc.payload) < 4000 {
			seeds = append(seeds, []byte(tc.payload))
		}
	}
	alphabet := []byte(`{}[]",:\ 0123456789.-eEtfnulaAbMmZT` + "\xff\x80")
	rng := rand.New(rand.NewSource(1))
	for _, seed := range seeds {
		if len(seed) == 0 {
			continue
		}
		for i := 0; i < 60; i++ {
			mutated := append([]byte(nil), seed...)
			for k := rng.Intn(3) + 1; k > 0; k-- {
				pos := rng.Intn(len(mutated))
				switch rng.Intn(3) {
				case 0:
					mutated[pos] = alphabet[rng.Intn(len(alphabet))]
				case 1:
					mutated = append(mutated[:pos], mutated[pos+1:]...)
				default:
					mutated = append(mutated[:pos], append([]byte{alphabet[rng.Intn(len(alphabet))]}, mutated[pos:]...)...)
				}
				if len(mutated) == 0 {
					break
				}
			}
			requireSameDecode(t, mutated)
		}
	}
}

// FuzzDecodeSinglePass extends the same property to anything the fuzzer finds. Under plain
// `go test` it runs the seed corpus.
func FuzzDecodeSinglePass(f *testing.F) {
	for _, tc := range equivCases() {
		if len(tc.payload) < 4000 {
			f.Add([]byte(tc.payload))
		}
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		requireSameDecode(t, payload)
	})
}

func TestCanonicalKeys(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want bool
	}{
		{`{}`, true},
		{`{"a":1,"b":{"c":2},"d":[{"e":1},{"e":2}]}`, true}, // the same key in sibling objects is fine
		{`{"a":1,"a":2}`, false},
		{`{"a":1,"A":2}`, false},
		{`{"a":{"b":1,"B":2}}`, false},
		{`[{"a":1,"a":2}]`, false},
		{`{"a":"x","b":"a","c":"a"}`, true}, // equal VALUES are not keys
		{`{"a":"{\"b\":1,\"b\":2}"}`, true}, // a key-shaped string inside a value
		{`{"a":"say \"hi\"","b":1}`, true},  // an escaped quote must not end the string early
		{`{"a\"":1}`, false},                // escaped key
		{"{\"\xc3\xa9\":1}", false},         // non-ascii key
		{`["a","a"]`, true},                 // array elements are not keys
		{`{"a":["x","a"]}`, true},           // nor are strings in an array that is an object's value
		{`{"a":1e400}`, false},
		{`{"a":1e-400}`, true},
		{`{"a":[1.5E+3]}`, false},
		{`{"a":1}}`, false}, // not valid JSON: declined, not followed
		{`{"a":[1}`, false},
		{`{"a":"x`, false},
		{`{"a":1`, false},
		{`{"a":["x",{"b":1}],"c":1}`, true},
		{`{"a":1}`, true},
	} {
		require.Equal(t, tc.want, canonicalKeys([]byte(tc.doc)), tc.doc)
	}
	// The hashed branch.
	require.True(t, canonicalKeys([]byte(bigObject(500))))
	require.False(t, canonicalKeys([]byte(strings.Replace(bigObject(500), `"m0499"`, `"M0001"`, 1))))
}
