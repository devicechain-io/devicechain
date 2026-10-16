// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"google.golang.org/protobuf/proto"
)

// referenceMarshalMeasurements is the implementation the direct wire writer replaced, kept
// verbatim as the oracle: the protobuf runtime's own encoding of the same message.
func referenceMarshalMeasurements(payload *model.UnresolvedMeasurementsPayload) ([]byte, error) {
	pbpayload := &PUnresolvedMeasurementsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime)
		pbentry := &PUnresolvedMeasurementsEntry{
			Measurements: entry.Measurements,
			Occurred:     nanos,
			OccurredTime: text,
		}
		pbpayload.Entries = append(pbpayload.Entries, pbentry)
	}
	return proto.Marshal(pbpayload)
}

// readings builds n readings with names and values of the given lengths.
func readings(n, nameLen, valueLen int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("m%04d", i)
		if pad := nameLen - len(name); pad > 0 {
			name += strings.Repeat("n", pad)
		}
		m[name] = strings.Repeat("7", valueLen)
	}
	return m
}

func measurementShapes() map[string]*model.UnresolvedMeasurementsPayload {
	utc := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	whole := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	offset := time.Date(2026, 10, 1, 14, 30, 0, 5000, time.FixedZone("x", 2*3600+30*60))
	epoch := time.Unix(0, 0)
	farFuture := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	zero := time.Time{}
	farOffset := time.Date(9999, 12, 30, 1, 2, 3, 5, time.FixedZone("z", 2*3600))
	entry := func(m map[string]string, at *time.Time) model.UnresolvedMeasurementsEntry {
		return model.UnresolvedMeasurementsEntry{Measurements: m, OccurredTime: at}
	}
	one := func(e ...model.UnresolvedMeasurementsEntry) *model.UnresolvedMeasurementsPayload {
		return &model.UnresolvedMeasurementsPayload{Entries: e}
	}
	many := make([]model.UnresolvedMeasurementsEntry, 9)
	for i := range many {
		at := utc.Add(time.Duration(i) * time.Second)
		var t *time.Time
		if i%3 != 0 {
			t = &at
		}
		many[i] = entry(readings(i, 6, i), t)
	}
	return map[string]*model.UnresolvedMeasurementsPayload{
		"no entries":                         one(),
		"nil entries":                        {Entries: nil},
		"one empty entry":                    one(entry(nil, nil)),
		"one entry, empty map":               one(entry(map[string]string{}, nil)),
		"time only":                          one(entry(nil, &utc)),
		"one reading":                        one(entry(map[string]string{"cpu": "42.17"}, nil)),
		"one reading, timed":                 one(entry(map[string]string{"cpu": "42.17"}, &utc)),
		"whole-second time":                  one(entry(map[string]string{"cpu": "1"}, &whole)),
		"non-UTC time":                       one(entry(map[string]string{"cpu": "1"}, &offset)),
		"the epoch itself":                   one(entry(map[string]string{"cpu": "1"}, &epoch)),
		"past the count (string)":            one(entry(map[string]string{"cpu": "1"}, &farFuture)),
		"zero time (string)":                 one(entry(map[string]string{"cpu": "1"}, &zero)),
		"offset past the count (UTC string)": one(entry(map[string]string{"cpu": "1"}, &farOffset)),
		"count then string":                  one(entry(map[string]string{"a": "1"}, &utc), entry(nil, &farFuture), entry(nil, &whole)),
		"empty name and value":               one(entry(map[string]string{"": ""}, &utc)),
		"empty value":                        one(entry(map[string]string{"cpu": ""}, nil)),
		"unicode":                            one(entry(map[string]string{"température": "21,5 °C", "湿度": "40%"}, &utc)),
		"ten readings":                       one(entry(readings(10, 8, 4), &utc)),
		"max readings":                       one(entry(readings(256, 8, 6), &utc)),
		"long value (2-byte length)":         one(entry(map[string]string{"blob": strings.Repeat("x", 200)}, &utc)),
		"long value (3-byte length)":         one(entry(map[string]string{"blob": strings.Repeat("x", 20000)}, &utc)),
		"item over 127 bytes":                one(entry(map[string]string{strings.Repeat("k", 70): strings.Repeat("v", 70)}, nil)),
		"entry over 127 bytes":               one(entry(readings(12, 8, 6), &utc)),
		"several entries":                    one(entry(readings(2, 4, 2), &utc), entry(nil, nil), entry(readings(3, 5, 3), &whole)),
		"many entries, scratch grows":        one(many...),
	}
}

// TestMeasurementsPayloadWireMatchesProtoMarshal holds the direct writer to the protobuf
// runtime's encoding of the same message, judged by the runtime's own decoder: both byte
// strings must decode, to equal messages. Where map iteration order cannot differ (at most
// one reading per entry) the bytes themselves must be identical.
func TestMeasurementsPayloadWireMatchesProtoMarshal(t *testing.T) {
	for name, payload := range measurementShapes() {
		t.Run(name, func(t *testing.T) {
			assertWireMatchesReference(t, payload)
		})
	}
}

func assertWireMatchesReference(t *testing.T, payload *model.UnresolvedMeasurementsPayload) {
	t.Helper()
	got, err := MarshalPayloadForMeasurementsEvent(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := referenceMarshalMeasurements(payload)
	if err != nil {
		t.Fatalf("reference marshal: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("encoded %d bytes; the protobuf runtime encodes %d", len(got), len(want))
	}
	var gotMsg, wantMsg PUnresolvedMeasurementsPayload
	if err := proto.Unmarshal(got, &gotMsg); err != nil {
		t.Fatalf("the protobuf runtime cannot decode the written bytes: %v", err)
	}
	if err := proto.Unmarshal(want, &wantMsg); err != nil {
		t.Fatalf("reference does not decode: %v", err)
	}
	if !proto.Equal(&gotMsg, &wantMsg) {
		t.Fatalf("decoded message differs:\n got %v\nwant %v", &gotMsg, &wantMsg)
	}
	ordered := true
	for _, e := range payload.Entries {
		ordered = ordered && len(e.Measurements) <= 1
	}
	if ordered && !bytes.Equal(got, want) {
		t.Fatalf("bytes differ where no map order can:\n got %x\nwant %x", got, want)
	}
}

// TestMeasurementsPayloadWireRoundTrips reads the written bytes back through the decoder
// device-management uses, and gets the payload that was written: every reading, and each
// entry's time to the nanosecond, in UTC.
func TestMeasurementsPayloadWireRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 1, 14, 30, 0, 123456789, time.FixedZone("x", 2*3600))
	in := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
		{Measurements: map[string]string{"cpu": "42.17", "mem": "", "": "x"}, OccurredTime: &at},
		{Measurements: map[string]string{"cpu": "43"}},
	}}
	encoded, err := MarshalPayloadForMeasurementsEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalPayloadForMeasurementsEvent(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("decoded %d entries; want 2", len(out.Entries))
	}
	for i, e := range out.Entries {
		if fmt.Sprint(e.Measurements) != fmt.Sprint(in.Entries[i].Measurements) {
			t.Errorf("entry %d readings %v; want %v", i, e.Measurements, in.Entries[i].Measurements)
		}
	}
	if got := out.Entries[0].OccurredTime; got == nil || !got.Equal(at) || got.Location() != time.UTC {
		t.Errorf("entry 0 time %v; want %v in UTC", got, at)
	}
	if out.Entries[1].OccurredTime != nil {
		t.Errorf("entry 1 time %v; want none (the device timed only the message)", out.Entries[1].OccurredTime)
	}
}

// TestMeasurementsPayloadWireRefusesInvalidUTF8 holds the refusal the protobuf runtime
// makes of a proto3 string that is not UTF-8, for a reading's name and for its value.
func TestMeasurementsPayloadWireRefusesInvalidUTF8(t *testing.T) {
	bad := string([]byte{0xff, 0xfe})
	for name, m := range map[string]map[string]string{
		"name":  {bad: "1"},
		"value": {"cpu": bad},
	} {
		t.Run(name, func(t *testing.T) {
			payload := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"ok": "1"}}, {Measurements: m},
			}}
			if _, err := referenceMarshalMeasurements(payload); err == nil {
				t.Fatal("the protobuf runtime accepted it: this test's premise no longer holds")
			}
			got, err := MarshalPayloadForMeasurementsEvent(payload)
			if !errors.Is(err, errMeasurementsNotUTF8) {
				t.Fatalf("marshal = %x, %v; want the UTF-8 refusal", got, err)
			}
		})
	}
}

// TestMeasurementsPayloadWireAllocatesOnlyItsOutput pins what the direct writer is for: a
// payload of one timed entry — every device's shape — costs exactly one allocation, the
// output buffer, whatever its reading count. Through the protobuf runtime a one-reading
// payload cost ten and a ten-reading one forty-six, most of them the reflection the map
// field is marshalled with; a change that brings any of that back fails here.
func TestMeasurementsPayloadWireAllocatesOnlyItsOutput(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 123000000, time.UTC)
	for _, n := range []int{1, 10, 256} {
		payload := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
			{Measurements: readings(n, 8, 5), OccurredTime: &at},
		}}
		allocs := testing.AllocsPerRun(100, func() {
			if _, err := MarshalPayloadForMeasurementsEvent(payload); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 1 {
			t.Errorf("%d readings: %.0f allocations per marshal; want 1 (the output)", n, allocs)
		}
	}
}

// FuzzMeasurementsPayloadWire holds the writer to the runtime on arbitrary names, values
// and times: both refuse, or both encode to messages that decode equal.
func FuzzMeasurementsPayloadWire(f *testing.F) {
	f.Add("cpu", "42.17", "mem", "", int64(1759320000), int64(5), true)
	f.Add("", "", "température", "21,5", int64(0), int64(0), false)
	f.Add(string([]byte{0xff}), "1", "a", "b", int64(-1), int64(999999999), true)
	f.Add(strings.Repeat("k", 130), strings.Repeat("v", 300), "x", "y", int64(253402300799), int64(1), true)
	f.Fuzz(func(t *testing.T, n1, v1, n2, v2 string, sec, nsec int64, timed bool) {
		var at *time.Time
		if timed {
			ts := time.Unix(sec%253402300800, nsec%1e9).UTC()
			at = &ts
		}
		payload := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
			{Measurements: map[string]string{n1: v1, n2: v2}, OccurredTime: at},
			{Measurements: map[string]string{n2: v1}},
		}}
		_, refErr := referenceMarshalMeasurements(payload)
		_, err := MarshalPayloadForMeasurementsEvent(payload)
		if (refErr == nil) != (err == nil) {
			t.Fatalf("runtime err %v, writer err %v: they must refuse the same payloads", refErr, err)
		}
		if err == nil {
			assertWireMatchesReference(t, payload)
		}
	})
}

// BenchmarkMeasurementsPayloadMarshal compares the direct writer with the protobuf runtime
// (the reference it replaced) on the shapes ingest sees: one reading, the benchmark
// fleet's, and ten.
//
// Run with: go test ./proto -run '^$' -bench MeasurementsPayloadMarshal -benchmem -count 10 -cpu 1
func BenchmarkMeasurementsPayloadMarshal(b *testing.B) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 123000000, time.UTC)
	impls := []struct {
		name string
		fn   func(*model.UnresolvedMeasurementsPayload) ([]byte, error)
	}{
		{"reference", referenceMarshalMeasurements},
		{"wire", MarshalPayloadForMeasurementsEvent},
	}
	for _, n := range []int{1, 10} {
		payload := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
			{Measurements: readings(n, 8, 5), OccurredTime: &at},
		}}
		for _, impl := range impls {
			b.Run(fmt.Sprintf("readings=%d/%s", n, impl.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := impl.fn(payload); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
