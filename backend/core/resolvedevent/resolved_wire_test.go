// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The oracle: MarshalResolvedEvent and MarshalPayloadForMeasurementsEvent as they were
// before the direct writer, building the generated message and handing it to
// proto.Marshal. The writer must produce the same bytes for every input. There is no map
// anywhere in a resolved event, so "the same bytes" is exact, not "the same message".

func referenceOptionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// referenceInstant is an instant as the oracle writes it, decided here rather than by the
// code it judges. Legacy is the string, exactly as the release before the count formatted
// it: an entry's in UTC, the envelope's in its own offset. Compact is the count when it
// holds the instant, else the string in UTC.
func referenceInstant(at time.Time, format eventtime.WireFormat, entry bool) (*int64, *string) {
	if format == eventtime.WireCompact {
		if n, ok := eventtime.WireNanos(at); ok {
			return &n, nil
		}
	}
	s := oracleString(at, format == eventtime.WireLegacy && !entry)
	return nil, &s
}

// oracleString is the string form, restated here rather than shared with the code it judges:
// RFC 3339 carries the years 0000 to 9999 in the offset the string is written in, so the
// preferred offset (UTC, or for a legacy envelope the instant's own) is used when it can
// carry the instant, and the other one otherwise.
func oracleString(at time.Time, ownOffset bool) string {
	fits := func(t time.Time) bool { return t.Year() >= 0 && t.Year() <= 9999 }
	first, second := at.UTC(), at
	if ownOffset {
		first, second = at, at.UTC()
	}
	if fits(first) || !fits(second) {
		return first.Format(time.RFC3339Nano)
	}
	return second.Format(time.RFC3339Nano)
}

// bothFormats is every format a producer writes, which every equivalence below holds.
var bothFormats = []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact}

func referenceMarshalMeasurementsPayload(payload *ResolvedMeasurementsPayload, format eventtime.WireFormat) ([]byte, error) {
	pbpayload := &PResolvedMeasurementsPayload{}
	for _, mxsentry := range payload.Entries {
		pmxentries := make([]*PResolvedMeasurementEntry, 0)
		for _, mxentry := range mxsentry.Entries {
			pmxentries = append(pmxentries, &PResolvedMeasurementEntry{
				Name:       mxentry.Name,
				Value:      mxentry.Value,
				Classifier: mxentry.Classifier,
				Unit:       mxentry.Unit,
				DataType:   mxentry.DataType,
				Derived:    mxentry.Derived,
			})
		}
		nanos, text := referenceInstant(mxsentry.OccurredTime, format, true)
		pbpayload.Entries = append(pbpayload.Entries, &PResolvedMeasurementsEntry{
			Measurements: pmxentries,
			Occurred:     nanos,
			OccurredTime: text,
		})
	}
	return proto.Marshal(pbpayload)
}

func referenceMarshalResolvedEvent(event *ResolvedEvent, format eventtime.WireFormat) ([]byte, error) {
	var pybytes []byte
	var err error
	if mx, ok := event.Payload.(*ResolvedMeasurementsPayload); ok && event.EventType == eventtype.Measurement {
		pybytes, err = referenceMarshalMeasurementsPayload(mx, format)
	} else {
		pybytes, err = MarshalResolvedPayload(event.EventType, event.Payload, format)
	}
	if err != nil {
		return nil, err
	}
	anchors := make([]*PResolvedAnchor, 0, len(event.Anchors))
	for _, a := range event.Anchors {
		anchors = append(anchors, &PResolvedAnchor{
			AnchorType:     a.AnchorType,
			AnchorToken:    a.AnchorToken,
			RelationshipId: uint64(a.RelationshipId),
		})
	}
	memberships := make([]*PScopeMembership, 0, len(event.ScopeMemberships))
	for _, m := range event.ScopeMemberships {
		memberships = append(memberships, &PScopeMembership{GroupToken: m.GroupToken, Version: m.Version})
	}
	occurred, occurredText := referenceInstant(event.OccurredTime, format, false)
	processed, processedText := referenceInstant(event.ProcessedTime, format, false)
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return proto.Marshal(&PResolvedEvent{
		Source:               event.Source,
		AltId:                event.AltId,
		SourceDeviceToken:    event.SourceDeviceToken,
		Anchors:              anchors,
		DeviceTypeToken:      referenceOptionalString(event.DeviceTypeToken),
		ProfileVersionToken:  referenceOptionalString(event.ProfileVersionToken),
		ScopeMemberships:     memberships,
		ExternalId:           referenceOptionalString(event.ExternalId),
		FenceSetVersion:      event.FenceSetVersion,
		SourceDeviceDisabled: event.SourceDeviceDisabled,
		Occurred:             occurred,
		OccurredTime:         deref(occurredText),
		Processed:            processed,
		ProcessedTime:        deref(processedText),
		EventType:            int64(event.EventType),
		Payload:              pybytes,
	})
}

func strp(s string) *string { return &s }
func u64p(v uint64) *uint64 { return &v }

// readings is a measurements entry of n readings whose name and value lengths step
// through every varint length boundary a realistic reading reaches.
func readings(n int) []ResolvedMeasurementEntry {
	out := make([]ResolvedMeasurementEntry, 0, n)
	for i := 0; i < n; i++ {
		mx := ResolvedMeasurementEntry{Name: fmt.Sprintf("m%d", i), Value: fmt.Sprintf("%d.25", i)}
		switch i % 4 {
		case 1:
			mx.Classifier, mx.Unit, mx.DataType = u64p(uint64(i)), strp("Cel"), strp("DOUBLE")
		case 2:
			mx.Classifier = u64p(1 << 63)
		case 3:
			mx.Unit = strp("")
		}
		out = append(out, mx)
	}
	return out
}

// resolvedShapes is every shape the equivalence test holds against the oracle.
func resolvedShapes() map[string]*ResolvedEvent {
	at := time.Date(2026, 10, 12, 9, 30, 15, 123456789, time.UTC)
	whole := time.Date(2026, 10, 12, 9, 30, 15, 0, time.UTC)
	offset := time.Date(2026, 10, 12, 9, 30, 15, 500, time.FixedZone("x", -7*3600-30*60))
	mx := func(entries ...ResolvedMeasurementsEntry) *ResolvedMeasurementsPayload {
		return &ResolvedMeasurementsPayload{Entries: entries}
	}
	base := func() *ResolvedEvent {
		return &ResolvedEvent{
			Source: "http", SourceDeviceToken: "dev-1", DeviceTypeToken: "sensor",
			ProfileVersionToken: "p@3", OccurredTime: at, ProcessedTime: at.Add(time.Millisecond),
			EventType: eventtype.Measurement,
			Payload: mx(ResolvedMeasurementsEntry{
				Entries: []ResolvedMeasurementEntry{{Name: "temp", Value: "21.5"}}, OccurredTime: at}),
		}
	}
	with := func(f func(*ResolvedEvent)) *ResolvedEvent { e := base(); f(e); return e }
	long := strings.Repeat("x", 200)
	longer := strings.Repeat("y", 20000)

	return map[string]*ResolvedEvent{
		"loadtest shape": base(),
		"zero event":     {EventType: eventtype.Measurement, Payload: mx()},
		"every field": with(func(e *ResolvedEvent) {
			e.AltId = strp("alt-1")
			e.Anchors = []ResolvedAnchor{
				{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 5},
				{AnchorType: "area", AnchorToken: "w-3", RelationshipId: 1 << 40},
			}
			e.ScopeMemberships = []GroupRef{{GroupToken: "g1", Version: 2}, {GroupToken: "g2", Version: 1 << 30}}
			e.ExternalId = "grp/node/dev"
			e.FenceSetVersion = 7
			e.SourceDeviceDisabled = true
		}),
		"empty alt id is present": with(func(e *ResolvedEvent) { e.AltId = strp("") }),
		"empty anchor and membership": with(func(e *ResolvedEvent) {
			e.Anchors = []ResolvedAnchor{{}}
			e.ScopeMemberships = []GroupRef{{}}
		}),
		"negative int32s sign-extend": with(func(e *ResolvedEvent) {
			e.ScopeMemberships = []GroupRef{{GroupToken: "g", Version: -1}, {Version: -1 << 31}}
			e.FenceSetVersion = -3
		}),
		"no tokens": with(func(e *ResolvedEvent) {
			e.Source, e.SourceDeviceToken, e.DeviceTypeToken, e.ProfileVersionToken = "", "", "", ""
		}),
		"unicode": with(func(e *ResolvedEvent) {
			e.Source, e.ExternalId = "源", "grün/\U0001F600"
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at,
				Entries: []ResolvedMeasurementEntry{{Name: "température", Value: "２１", Unit: strp("°C")}}})
		}),
		"non-UTC and whole-second times": with(func(e *ResolvedEvent) {
			e.OccurredTime, e.ProcessedTime = offset, whole
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: offset, Entries: readings(1)},
				ResolvedMeasurementsEntry{OccurredTime: whole, Entries: readings(1)})
		}),
		"instants past the count (string form)": with(func(e *ResolvedEvent) {
			far := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
			e.OccurredTime = far
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: far, Entries: readings(1)},
				ResolvedMeasurementsEntry{OccurredTime: at, Entries: readings(1)})
		}),
		"an offset instant past the count (string form, in UTC)": with(func(e *ResolvedEvent) {
			far := time.Date(9999, 12, 30, 1, 2, 3, 5, time.FixedZone("z", 2*3600))
			e.ProcessedTime = far
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: far, Entries: readings(1)})
		}),
		"the epoch itself is a count": with(func(e *ResolvedEvent) {
			e.OccurredTime, e.ProcessedTime = time.Unix(0, 0), time.Unix(0, 0)
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: time.Unix(0, 0), Entries: readings(1)})
		}),
		"zero times": with(func(e *ResolvedEvent) {
			e.OccurredTime, e.ProcessedTime = time.Time{}, time.Time{}
			e.Payload = mx(ResolvedMeasurementsEntry{Entries: readings(1)})
		}),
		"empty entry and empty readings": with(func(e *ResolvedEvent) {
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at},
				ResolvedMeasurementsEntry{OccurredTime: at, Entries: []ResolvedMeasurementEntry{{}}})
		}),
		"zero classifier and empty unit are present": with(func(e *ResolvedEvent) {
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at, Entries: []ResolvedMeasurementEntry{
				{Name: "a", Classifier: u64p(0), Unit: strp(""), DataType: strp("")}}})
		}),
		"10 readings": with(func(e *ResolvedEvent) {
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at, Entries: readings(10)})
		}),
		"256 readings": with(func(e *ResolvedEvent) {
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at, Entries: readings(256)})
		}),
		"many entries": with(func(e *ResolvedEvent) {
			var entries []ResolvedMeasurementsEntry
			for i := 0; i < 9; i++ {
				entries = append(entries, ResolvedMeasurementsEntry{OccurredTime: at.Add(time.Duration(i) * time.Second), Entries: readings(i)})
			}
			e.Payload = mx(entries...)
		}),
		"2- and 3-byte lengths": with(func(e *ResolvedEvent) {
			e.Source, e.ExternalId = long, longer
			e.Anchors = []ResolvedAnchor{{AnchorType: long, AnchorToken: longer}}
			e.Payload = mx(ResolvedMeasurementsEntry{OccurredTime: at, Entries: []ResolvedMeasurementEntry{
				{Name: long, Value: longer, Unit: strp(long)}}})
		}),
		"location": with(func(e *ResolvedEvent) {
			e.EventType, e.FenceSetVersion = eventtype.Location, 4
			e.Payload = &ResolvedLocationsPayload{Entries: []ResolvedLocationEntry{
				{Latitude: strp("1.5"), Longitude: strp("-2.25"), OccurredTime: at}}}
		}),
		"alert": with(func(e *ResolvedEvent) {
			e.EventType = eventtype.Alert
			e.Payload = &ResolvedAlertsPayload{Entries: []ResolvedAlertEntry{
				{Type: "t", Level: 2, Message: "m", Source: "s", OccurredTime: at}}}
		}),
		"state change": with(func(e *ResolvedEvent) {
			e.EventType = eventtype.StateChange
			e.Payload = &ResolvedStateChangePayload{State: "online", SessionId: 11, OccurredTime: strp("2026-10-12T00:00:00Z")}
		}),
		"new relationship": with(func(e *ResolvedEvent) {
			e.EventType = eventtype.NewRelationship
			e.Payload = &ResolvedNewRelationshipPayload{RelationshipTypeId: 3, TargetType: strp("area"), TargetId: u64p(9)}
		}),
		"measurements with an empty payload": with(func(e *ResolvedEvent) { e.Payload = mx() }),
	}
}

// TestResolvedEventWireMatchesProtoMarshal holds the writer to the oracle, byte for byte,
// and to every consumer's decoder.
func TestResolvedEventWireMatchesProtoMarshal(t *testing.T) {
	for name, event := range resolvedShapes() {
		for _, format := range bothFormats {
			t.Run(name+"/"+format.String(), func(t *testing.T) {
				assertResolvedWireMatchesReference(t, event, format)
			})
		}
	}
}

func assertResolvedWireMatchesReference(t *testing.T, event *ResolvedEvent, format eventtime.WireFormat) {
	t.Helper()
	{
		{
			want, err := referenceMarshalResolvedEvent(event, format)
			require.NoError(t, err)
			got, err := MarshalResolvedEventAs(event, format)
			require.NoError(t, err)
			require.Equal(t, want, got, "the writer's bytes differ from proto.Marshal's")

			// Decoded the way every consumer decodes it, the two are the same event.
			wantEv, err := UnmarshalResolvedEvent(want)
			require.NoError(t, err)
			gotEv, err := UnmarshalResolvedEvent(got)
			require.NoError(t, err)
			assert.Equal(t, wantEv, gotEv)

			if mx, ok := event.Payload.(*ResolvedMeasurementsPayload); ok {
				wantPayload, err := referenceMarshalMeasurementsPayload(mx, format)
				require.NoError(t, err)
				gotPayload, err := MarshalPayloadForMeasurementsEvent(mx, format)
				require.NoError(t, err)
				require.Equal(t, wantPayload, gotPayload, "the payload writer's bytes differ from proto.Marshal's")
			}
		}
	}
}

// TestResolvedEventWireRefusesWhatProtoMarshalRefuses: a payload neither path can marshal
// is refused by both.
func TestResolvedEventWireRefusesWhatProtoMarshalRefuses(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for name, event := range map[string]*ResolvedEvent{
		"measurement without a payload":      {EventType: eventtype.Measurement, OccurredTime: at},
		"measurement with another's payload": {EventType: eventtype.Measurement, Payload: &ResolvedAlertsPayload{}},
		// The inverse: the writer takes the in-place path only for a MEASUREMENT event, so
		// a measurements payload under any other type is refused as the runtime refuses it,
		// never written as though the type had been right.
		"another type with a measurements payload": {EventType: eventtype.Alert, OccurredTime: at,
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: []ResolvedMeasurementEntry{{Name: "n", Value: "v"}}}}}},
		"unknown event type": {EventType: eventtype.EventType(99)},
	} {
		t.Run(name, func(t *testing.T) {
			for _, format := range bothFormats {
				_, refErr := referenceMarshalResolvedEvent(event, format)
				require.Error(t, refErr, "the oracle accepts this; the test's premise is wrong")
				_, err := MarshalResolvedEventAs(event, format)
				require.Error(t, err, format)
			}
		})
	}
}

// TestResolvedEventWireRefusesInvalidUTF8 puts an invalid UTF-8 string into every string
// field in turn. proto.Marshal refuses each — asserted first, so the premise is pinned —
// and the writer must refuse each too.
func TestResolvedEventWireRefusesInvalidUTF8(t *testing.T) {
	const bad = "ok\xff"
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	fresh := func() *ResolvedEvent {
		return &ResolvedEvent{
			EventType: eventtype.Measurement, OccurredTime: at, ProcessedTime: at,
			Anchors:          []ResolvedAnchor{{AnchorType: "a", AnchorToken: "t"}},
			ScopeMemberships: []GroupRef{{GroupToken: "g"}},
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: []ResolvedMeasurementEntry{{Name: "n", Value: "v"}}}}},
		}
	}
	reading := func(e *ResolvedEvent) *ResolvedMeasurementEntry {
		return &e.Payload.(*ResolvedMeasurementsPayload).Entries[0].Entries[0]
	}
	for name, spoil := range map[string]func(*ResolvedEvent){
		"source":                func(e *ResolvedEvent) { e.Source = bad },
		"alt id":                func(e *ResolvedEvent) { e.AltId = strp(bad) },
		"source device token":   func(e *ResolvedEvent) { e.SourceDeviceToken = bad },
		"anchor type":           func(e *ResolvedEvent) { e.Anchors[0].AnchorType = bad },
		"anchor token":          func(e *ResolvedEvent) { e.Anchors[0].AnchorToken = bad },
		"device type token":     func(e *ResolvedEvent) { e.DeviceTypeToken = bad },
		"profile version token": func(e *ResolvedEvent) { e.ProfileVersionToken = bad },
		"membership group":      func(e *ResolvedEvent) { e.ScopeMemberships[0].GroupToken = bad },
		"external id":           func(e *ResolvedEvent) { e.ExternalId = bad },
		"reading name":          func(e *ResolvedEvent) { reading(e).Name = bad },
		"reading value":         func(e *ResolvedEvent) { reading(e).Value = bad },
		"reading unit":          func(e *ResolvedEvent) { reading(e).Unit = strp(bad) },
		"reading data type":     func(e *ResolvedEvent) { reading(e).DataType = strp(bad) },
	} {
		t.Run(name, func(t *testing.T) {
			event := fresh()
			_, err := referenceMarshalResolvedEvent(event, eventtime.WireCompact)
			require.NoError(t, err)
			spoil(event)
			_, err = referenceMarshalResolvedEvent(event, eventtime.WireCompact)
			require.Error(t, err, "proto.Marshal no longer refuses invalid UTF-8 here: the writer's refusal has no premise")
			for _, format := range bothFormats {
				_, err = MarshalResolvedEventAs(event, format)
				require.ErrorIs(t, err, errResolvedNotUTF8, format)
			}
			if strings.HasPrefix(name, "reading") {
				_, err = MarshalPayloadForMeasurementsEvent(event.Payload.(*ResolvedMeasurementsPayload), eventtime.WireCompact)
				require.ErrorIs(t, err, errResolvedNotUTF8)
			}
		})
	}
}

// TestResolvedEventWireAllocatesOnlyItsOutput is the point of the writer: a measurement
// event of one entry — what every device sends — costs exactly one allocation, its output,
// however many readings, anchors and memberships it carries.
func TestResolvedEventWireAllocatesOnlyItsOutput(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 30, 15, 123456789, time.UTC)
	for _, n := range []int{1, 10, 256} {
		event := &ResolvedEvent{
			Source: "http", AltId: strp("alt"), SourceDeviceToken: "dev-1", DeviceTypeToken: "sensor",
			ProfileVersionToken: "p@3", ExternalId: "x",
			Anchors:          []ResolvedAnchor{{AnchorType: "area", AnchorToken: "a", RelationshipId: 1}},
			ScopeMemberships: []GroupRef{{GroupToken: "g", Version: 1}},
			OccurredTime:     at, ProcessedTime: at, EventType: eventtype.Measurement,
			Payload: &ResolvedMeasurementsPayload{Entries: []ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: readings(n)}}},
		}
		allocs := testing.AllocsPerRun(100, func() {
			if _, err := MarshalResolvedEventAs(event, eventtime.WireCompact); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 1 {
			t.Errorf("%d readings: %v allocations, want 1 (the output)", n, allocs)
		}
		payload := event.Payload.(*ResolvedMeasurementsPayload)
		allocs = testing.AllocsPerRun(100, func() {
			if _, err := MarshalPayloadForMeasurementsEvent(payload, eventtime.WireCompact); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 1 {
			t.Errorf("%d readings, payload alone: %v allocations, want 1 (the output)", n, allocs)
		}
	}
}

// FuzzResolvedEventWire builds a resolved event from arbitrary input and requires the
// writer and proto.Marshal to agree exactly: the same bytes, or both refusing. kind picks
// the event type and payload: a measurement (written in place), every other type with its
// own payload (marshalled by the runtime and copied in), a measurements payload under
// another type, and an arbitrary event type.
func FuzzResolvedEventWire(f *testing.F) {
	f.Add("http", "dev", "alt", "a", "t", "g", "21.5", "Cel", uint64(7), int64(1), int32(2), int32(0), uint8(1), uint8(1), uint8(0x3f), uint8(0))
	f.Add("", "", "", "", "", "", "", "", uint64(0), int64(-1), int32(-1), int32(-5), uint8(0), uint8(3), uint8(0), uint8(0))
	f.Add("ok\xff", "d", "", "", "", "", "v", "", uint64(1<<63), int64(1<<40), int32(1<<31-1), int32(9), uint8(4), uint8(0), uint8(0xff), uint8(0))
	for kind := uint8(1); kind < 7; kind++ {
		f.Add("mqtt", "dev", "", "a", "t", "g", "1.5", "m", uint64(300), int64(5), int32(-2), int32(3), uint8(2), uint8(2), uint8(0x0f), kind)
	}
	f.Fuzz(func(t *testing.T, source, device, alt, anchor, token, group, value, unit string,
		classifier uint64, nanos int64, version, fence int32, entries, perEntry, flags, kind uint8) {
		at := time.Unix(1_760_000_000, nanos%1_000_000_000).In(time.FixedZone("z", int(version%50000)))
		event := &ResolvedEvent{
			Source: source, SourceDeviceToken: device, OccurredTime: at, ProcessedTime: at.UTC(),
			EventType: eventtype.Measurement, FenceSetVersion: fence,
		}
		if flags&1 != 0 {
			event.AltId = strp(alt)
		}
		if flags&2 != 0 {
			event.Anchors = []ResolvedAnchor{{AnchorType: anchor, AnchorToken: token, RelationshipId: uint(classifier)}, {}}
		}
		if flags&4 != 0 {
			event.ScopeMemberships = []GroupRef{{GroupToken: group, Version: version}}
		}
		if flags&8 != 0 {
			event.DeviceTypeToken, event.ProfileVersionToken, event.ExternalId = anchor, token, group
		}
		if flags&128 != 0 {
			event.SourceDeviceDisabled = true
		}
		payload := &ResolvedMeasurementsPayload{}
		for i := 0; i < int(entries%6); i++ {
			entry := ResolvedMeasurementsEntry{OccurredTime: at.Add(time.Duration(i) * time.Hour)}
			for j := 0; j < int(perEntry%5); j++ {
				mx := ResolvedMeasurementEntry{Name: fmt.Sprintf("%s%d", value, j), Value: value}
				if flags&16 != 0 {
					mx.Unit = strp(unit)
				}
				if flags&32 != 0 {
					mx.Classifier = u64p(classifier)
				}
				if flags&64 != 0 {
					mx.DataType = strp(source)
				}
				// Every flag bit is taken; the classifier's bits say which readings are derived.
				mx.Derived = (classifier>>uint(j))&1 == 1
				entry.Entries = append(entry.Entries, mx)
			}
			payload.Entries = append(payload.Entries, entry)
		}
		event.Payload = payload
		refused := false // refused whatever its strings hold
		switch kind % 7 {
		case 1:
			event.EventType = eventtype.Location
			event.Payload = &ResolvedLocationsPayload{Entries: []ResolvedLocationEntry{
				{Latitude: strp(value), Longitude: strp(unit), OccurredTime: at}}}
		case 2:
			event.EventType = eventtype.Alert
			event.Payload = &ResolvedAlertsPayload{Entries: []ResolvedAlertEntry{
				{Type: value, Level: uint32(classifier), Message: unit, Source: source, OccurredTime: at}}}
		case 3:
			event.EventType = eventtype.StateChange
			event.Payload = &ResolvedStateChangePayload{State: value, Reason: unit, SessionId: classifier}
		case 4:
			event.EventType = eventtype.NewRelationship
			event.Payload = &ResolvedNewRelationshipPayload{RelationshipTypeId: classifier, TargetType: strp(unit)}
		case 5:
			event.EventType, refused = eventtype.Alert, true // a measurements payload under another type
		case 6:
			event.EventType = eventtype.EventType(version)
			refused = event.EventType != eventtype.Measurement
		}

		var got []byte
		var err error
		for _, format := range bothFormats {
			want, wantErr := referenceMarshalResolvedEvent(event, format)
			got, err = MarshalResolvedEventAs(event, format)
			if (wantErr == nil) != (err == nil) {
				t.Fatalf("%s: proto.Marshal err %v, writer err %v", format, wantErr, err)
			}
			if err == nil && !bytes.Equal(want, got) {
				t.Fatalf("%s: bytes differ:\n proto.Marshal %x\n writer        %x", format, want, got)
			}
		}
		if refused && err == nil {
			t.Fatalf("event type %v with a measurements payload was written", event.EventType)
		}
		allValid := true
		for _, s := range []string{source, device, alt, anchor, token, group, value, unit} {
			allValid = allValid && utf8.ValidString(s)
		}
		if err != nil && allValid && !refused {
			t.Fatalf("refused valid UTF-8: %v", err)
		}
	})
}
