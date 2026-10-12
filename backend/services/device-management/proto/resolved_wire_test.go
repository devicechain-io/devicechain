// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
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

func referenceMarshalMeasurementsPayload(payload *model.ResolvedMeasurementsPayload) ([]byte, error) {
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
			})
		}
		pbpayload.Entries = append(pbpayload.Entries, &PResolvedMeasurementsEntry{
			Measurements: pmxentries,
			OccurredTime: marshalEntryTime(mxsentry.OccurredTime),
		})
	}
	return proto.Marshal(pbpayload)
}

func referenceMarshalResolvedEvent(event *model.ResolvedEvent) ([]byte, error) {
	var pybytes []byte
	var err error
	if mx, ok := event.Payload.(*model.ResolvedMeasurementsPayload); ok && event.EventType == esmodel.Measurement {
		pybytes, err = referenceMarshalMeasurementsPayload(mx)
	} else {
		pybytes, err = MarshalResolvedPayload(event.EventType, event.Payload)
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
	return proto.Marshal(&PResolvedEvent{
		Source:              event.Source,
		AltId:               event.AltId,
		SourceDeviceToken:   event.SourceDeviceToken,
		Anchors:             anchors,
		DeviceTypeToken:     referenceOptionalString(event.DeviceTypeToken),
		ProfileVersionToken: referenceOptionalString(event.ProfileVersionToken),
		ScopeMemberships:    memberships,
		ExternalId:          referenceOptionalString(event.ExternalId),
		FenceSetVersion:     event.FenceSetVersion,
		OccurredTime:        event.OccurredTime.Format(time.RFC3339Nano),
		ProcessedTime:       event.ProcessedTime.Format(time.RFC3339Nano),
		EventType:           int64(event.EventType),
		Payload:             pybytes,
	})
}

func strp(s string) *string { return &s }
func u64p(v uint64) *uint64 { return &v }

// readings is a measurements entry of n readings whose name and value lengths step
// through every varint length boundary a realistic reading reaches.
func readings(n int) []model.ResolvedMeasurementEntry {
	out := make([]model.ResolvedMeasurementEntry, 0, n)
	for i := 0; i < n; i++ {
		mx := model.ResolvedMeasurementEntry{Name: fmt.Sprintf("m%d", i), Value: fmt.Sprintf("%d.25", i)}
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
func resolvedShapes() map[string]*model.ResolvedEvent {
	at := time.Date(2026, 10, 12, 9, 30, 15, 123456789, time.UTC)
	whole := time.Date(2026, 10, 12, 9, 30, 15, 0, time.UTC)
	offset := time.Date(2026, 10, 12, 9, 30, 15, 500, time.FixedZone("x", -7*3600-30*60))
	mx := func(entries ...model.ResolvedMeasurementsEntry) *model.ResolvedMeasurementsPayload {
		return &model.ResolvedMeasurementsPayload{Entries: entries}
	}
	base := func() *model.ResolvedEvent {
		return &model.ResolvedEvent{
			Source: "http", SourceDeviceToken: "dev-1", DeviceTypeToken: "sensor",
			ProfileVersionToken: "p@3", OccurredTime: at, ProcessedTime: at.Add(time.Millisecond),
			EventType: esmodel.Measurement,
			Payload: mx(model.ResolvedMeasurementsEntry{
				Entries: []model.ResolvedMeasurementEntry{{Name: "temp", Value: "21.5"}}, OccurredTime: at}),
		}
	}
	with := func(f func(*model.ResolvedEvent)) *model.ResolvedEvent { e := base(); f(e); return e }
	long := strings.Repeat("x", 200)
	longer := strings.Repeat("y", 20000)

	return map[string]*model.ResolvedEvent{
		"loadtest shape": base(),
		"zero event":     {EventType: esmodel.Measurement, Payload: mx()},
		"every field": with(func(e *model.ResolvedEvent) {
			e.AltId = strp("alt-1")
			e.Anchors = []model.ResolvedAnchor{
				{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 5},
				{AnchorType: "area", AnchorToken: "w-3", RelationshipId: 1 << 40},
			}
			e.ScopeMemberships = []model.GroupRef{{GroupToken: "g1", Version: 2}, {GroupToken: "g2", Version: 1 << 30}}
			e.ExternalId = "grp/node/dev"
			e.FenceSetVersion = 7
		}),
		"empty alt id is present": with(func(e *model.ResolvedEvent) { e.AltId = strp("") }),
		"empty anchor and membership": with(func(e *model.ResolvedEvent) {
			e.Anchors = []model.ResolvedAnchor{{}}
			e.ScopeMemberships = []model.GroupRef{{}}
		}),
		"negative int32s sign-extend": with(func(e *model.ResolvedEvent) {
			e.ScopeMemberships = []model.GroupRef{{GroupToken: "g", Version: -1}, {Version: -1 << 31}}
			e.FenceSetVersion = -3
		}),
		"no tokens": with(func(e *model.ResolvedEvent) {
			e.Source, e.SourceDeviceToken, e.DeviceTypeToken, e.ProfileVersionToken = "", "", "", ""
		}),
		"unicode": with(func(e *model.ResolvedEvent) {
			e.Source, e.ExternalId = "源", "grün/\U0001F600"
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at,
				Entries: []model.ResolvedMeasurementEntry{{Name: "température", Value: "２１", Unit: strp("°C")}}})
		}),
		"non-UTC and whole-second times": with(func(e *model.ResolvedEvent) {
			e.OccurredTime, e.ProcessedTime = offset, whole
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: offset, Entries: readings(1)},
				model.ResolvedMeasurementsEntry{OccurredTime: whole, Entries: readings(1)})
		}),
		"zero times": with(func(e *model.ResolvedEvent) {
			e.OccurredTime, e.ProcessedTime = time.Time{}, time.Time{}
			e.Payload = mx(model.ResolvedMeasurementsEntry{Entries: readings(1)})
		}),
		"empty entry and empty readings": with(func(e *model.ResolvedEvent) {
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at},
				model.ResolvedMeasurementsEntry{OccurredTime: at, Entries: []model.ResolvedMeasurementEntry{{}}})
		}),
		"zero classifier and empty unit are present": with(func(e *model.ResolvedEvent) {
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at, Entries: []model.ResolvedMeasurementEntry{
				{Name: "a", Classifier: u64p(0), Unit: strp(""), DataType: strp("")}}})
		}),
		"10 readings": with(func(e *model.ResolvedEvent) {
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at, Entries: readings(10)})
		}),
		"256 readings": with(func(e *model.ResolvedEvent) {
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at, Entries: readings(256)})
		}),
		"many entries": with(func(e *model.ResolvedEvent) {
			var entries []model.ResolvedMeasurementsEntry
			for i := 0; i < 9; i++ {
				entries = append(entries, model.ResolvedMeasurementsEntry{OccurredTime: at.Add(time.Duration(i) * time.Second), Entries: readings(i)})
			}
			e.Payload = mx(entries...)
		}),
		"2- and 3-byte lengths": with(func(e *model.ResolvedEvent) {
			e.Source, e.ExternalId = long, longer
			e.Anchors = []model.ResolvedAnchor{{AnchorType: long, AnchorToken: longer}}
			e.Payload = mx(model.ResolvedMeasurementsEntry{OccurredTime: at, Entries: []model.ResolvedMeasurementEntry{
				{Name: long, Value: longer, Unit: strp(long)}}})
		}),
		"location": with(func(e *model.ResolvedEvent) {
			e.EventType, e.FenceSetVersion = esmodel.Location, 4
			e.Payload = &model.ResolvedLocationsPayload{Entries: []model.ResolvedLocationEntry{
				{Latitude: strp("1.5"), Longitude: strp("-2.25"), OccurredTime: at}}}
		}),
		"alert": with(func(e *model.ResolvedEvent) {
			e.EventType = esmodel.Alert
			e.Payload = &model.ResolvedAlertsPayload{Entries: []model.ResolvedAlertEntry{
				{Type: "t", Level: 2, Message: "m", Source: "s", OccurredTime: at}}}
		}),
		"state change": with(func(e *model.ResolvedEvent) {
			e.EventType = esmodel.StateChange
			e.Payload = &model.ResolvedStateChangePayload{State: "online", SessionId: 11, OccurredTime: strp("2026-10-12T00:00:00Z")}
		}),
		"new relationship": with(func(e *model.ResolvedEvent) {
			e.EventType = esmodel.NewRelationship
			e.Payload = &model.ResolvedNewRelationshipPayload{RelationshipTypeId: 3, TargetType: strp("area"), TargetId: u64p(9)}
		}),
		"measurements with an empty payload": with(func(e *model.ResolvedEvent) { e.Payload = mx() }),
	}
}

// TestResolvedEventWireMatchesProtoMarshal holds the writer to the oracle, byte for byte,
// and to every consumer's decoder.
func TestResolvedEventWireMatchesProtoMarshal(t *testing.T) {
	for name, event := range resolvedShapes() {
		t.Run(name, func(t *testing.T) {
			want, err := referenceMarshalResolvedEvent(event)
			require.NoError(t, err)
			got, err := MarshalResolvedEvent(event)
			require.NoError(t, err)
			require.Equal(t, want, got, "the writer's bytes differ from proto.Marshal's")

			// Decoded the way every consumer decodes it, the two are the same event.
			wantEv, err := UnmarshalResolvedEvent(want)
			require.NoError(t, err)
			gotEv, err := UnmarshalResolvedEvent(got)
			require.NoError(t, err)
			assert.Equal(t, wantEv, gotEv)

			if mx, ok := event.Payload.(*model.ResolvedMeasurementsPayload); ok {
				wantPayload, err := referenceMarshalMeasurementsPayload(mx)
				require.NoError(t, err)
				gotPayload, err := MarshalPayloadForMeasurementsEvent(mx)
				require.NoError(t, err)
				require.Equal(t, wantPayload, gotPayload, "the payload writer's bytes differ from proto.Marshal's")
			}
		})
	}
}

// TestResolvedEventWireRefusesWhatProtoMarshalRefuses: a payload neither path can marshal
// is refused by both.
func TestResolvedEventWireRefusesWhatProtoMarshalRefuses(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for name, event := range map[string]*model.ResolvedEvent{
		"measurement without a payload":      {EventType: esmodel.Measurement, OccurredTime: at},
		"measurement with another's payload": {EventType: esmodel.Measurement, Payload: &model.ResolvedAlertsPayload{}},
		// The inverse: the writer takes the in-place path only for a MEASUREMENT event, so
		// a measurements payload under any other type is refused as the runtime refuses it,
		// never written as though the type had been right.
		"another type with a measurements payload": {EventType: esmodel.Alert, OccurredTime: at,
			Payload: &model.ResolvedMeasurementsPayload{Entries: []model.ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: []model.ResolvedMeasurementEntry{{Name: "n", Value: "v"}}}}}},
		"unknown event type": {EventType: esmodel.EventType(99)},
	} {
		t.Run(name, func(t *testing.T) {
			_, refErr := referenceMarshalResolvedEvent(event)
			require.Error(t, refErr, "the oracle accepts this; the test's premise is wrong")
			_, err := MarshalResolvedEvent(event)
			require.Error(t, err)
		})
	}
}

// TestResolvedEventWireRefusesInvalidUTF8 puts an invalid UTF-8 string into every string
// field in turn. proto.Marshal refuses each — asserted first, so the premise is pinned —
// and the writer must refuse each too.
func TestResolvedEventWireRefusesInvalidUTF8(t *testing.T) {
	const bad = "ok\xff"
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	fresh := func() *model.ResolvedEvent {
		return &model.ResolvedEvent{
			EventType: esmodel.Measurement, OccurredTime: at, ProcessedTime: at,
			Anchors:          []model.ResolvedAnchor{{AnchorType: "a", AnchorToken: "t"}},
			ScopeMemberships: []model.GroupRef{{GroupToken: "g"}},
			Payload: &model.ResolvedMeasurementsPayload{Entries: []model.ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: []model.ResolvedMeasurementEntry{{Name: "n", Value: "v"}}}}},
		}
	}
	reading := func(e *model.ResolvedEvent) *model.ResolvedMeasurementEntry {
		return &e.Payload.(*model.ResolvedMeasurementsPayload).Entries[0].Entries[0]
	}
	for name, spoil := range map[string]func(*model.ResolvedEvent){
		"source":                func(e *model.ResolvedEvent) { e.Source = bad },
		"alt id":                func(e *model.ResolvedEvent) { e.AltId = strp(bad) },
		"source device token":   func(e *model.ResolvedEvent) { e.SourceDeviceToken = bad },
		"anchor type":           func(e *model.ResolvedEvent) { e.Anchors[0].AnchorType = bad },
		"anchor token":          func(e *model.ResolvedEvent) { e.Anchors[0].AnchorToken = bad },
		"device type token":     func(e *model.ResolvedEvent) { e.DeviceTypeToken = bad },
		"profile version token": func(e *model.ResolvedEvent) { e.ProfileVersionToken = bad },
		"membership group":      func(e *model.ResolvedEvent) { e.ScopeMemberships[0].GroupToken = bad },
		"external id":           func(e *model.ResolvedEvent) { e.ExternalId = bad },
		"reading name":          func(e *model.ResolvedEvent) { reading(e).Name = bad },
		"reading value":         func(e *model.ResolvedEvent) { reading(e).Value = bad },
		"reading unit":          func(e *model.ResolvedEvent) { reading(e).Unit = strp(bad) },
		"reading data type":     func(e *model.ResolvedEvent) { reading(e).DataType = strp(bad) },
	} {
		t.Run(name, func(t *testing.T) {
			event := fresh()
			_, err := referenceMarshalResolvedEvent(event)
			require.NoError(t, err)
			spoil(event)
			_, err = referenceMarshalResolvedEvent(event)
			require.Error(t, err, "proto.Marshal no longer refuses invalid UTF-8 here: the writer's refusal has no premise")
			_, err = MarshalResolvedEvent(event)
			require.ErrorIs(t, err, errResolvedNotUTF8)
			if strings.HasPrefix(name, "reading") {
				_, err = MarshalPayloadForMeasurementsEvent(event.Payload.(*model.ResolvedMeasurementsPayload))
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
		event := &model.ResolvedEvent{
			Source: "http", AltId: strp("alt"), SourceDeviceToken: "dev-1", DeviceTypeToken: "sensor",
			ProfileVersionToken: "p@3", ExternalId: "x",
			Anchors:          []model.ResolvedAnchor{{AnchorType: "area", AnchorToken: "a", RelationshipId: 1}},
			ScopeMemberships: []model.GroupRef{{GroupToken: "g", Version: 1}},
			OccurredTime:     at, ProcessedTime: at, EventType: esmodel.Measurement,
			Payload: &model.ResolvedMeasurementsPayload{Entries: []model.ResolvedMeasurementsEntry{
				{OccurredTime: at, Entries: readings(n)}}},
		}
		allocs := testing.AllocsPerRun(100, func() {
			if _, err := MarshalResolvedEvent(event); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 1 {
			t.Errorf("%d readings: %v allocations, want 1 (the output)", n, allocs)
		}
		payload := event.Payload.(*model.ResolvedMeasurementsPayload)
		allocs = testing.AllocsPerRun(100, func() {
			if _, err := MarshalPayloadForMeasurementsEvent(payload); err != nil {
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
		event := &model.ResolvedEvent{
			Source: source, SourceDeviceToken: device, OccurredTime: at, ProcessedTime: at.UTC(),
			EventType: esmodel.Measurement, FenceSetVersion: fence,
		}
		if flags&1 != 0 {
			event.AltId = strp(alt)
		}
		if flags&2 != 0 {
			event.Anchors = []model.ResolvedAnchor{{AnchorType: anchor, AnchorToken: token, RelationshipId: uint(classifier)}, {}}
		}
		if flags&4 != 0 {
			event.ScopeMemberships = []model.GroupRef{{GroupToken: group, Version: version}}
		}
		if flags&8 != 0 {
			event.DeviceTypeToken, event.ProfileVersionToken, event.ExternalId = anchor, token, group
		}
		payload := &model.ResolvedMeasurementsPayload{}
		for i := 0; i < int(entries%6); i++ {
			entry := model.ResolvedMeasurementsEntry{OccurredTime: at.Add(time.Duration(i) * time.Hour)}
			for j := 0; j < int(perEntry%5); j++ {
				mx := model.ResolvedMeasurementEntry{Name: fmt.Sprintf("%s%d", value, j), Value: value}
				if flags&16 != 0 {
					mx.Unit = strp(unit)
				}
				if flags&32 != 0 {
					mx.Classifier = u64p(classifier)
				}
				if flags&64 != 0 {
					mx.DataType = strp(source)
				}
				entry.Entries = append(entry.Entries, mx)
			}
			payload.Entries = append(payload.Entries, entry)
		}
		event.Payload = payload
		refused := false // refused whatever its strings hold
		switch kind % 7 {
		case 1:
			event.EventType = esmodel.Location
			event.Payload = &model.ResolvedLocationsPayload{Entries: []model.ResolvedLocationEntry{
				{Latitude: strp(value), Longitude: strp(unit), OccurredTime: at}}}
		case 2:
			event.EventType = esmodel.Alert
			event.Payload = &model.ResolvedAlertsPayload{Entries: []model.ResolvedAlertEntry{
				{Type: value, Level: uint32(classifier), Message: unit, Source: source, OccurredTime: at}}}
		case 3:
			event.EventType = esmodel.StateChange
			event.Payload = &model.ResolvedStateChangePayload{State: value, Reason: unit, SessionId: classifier}
		case 4:
			event.EventType = esmodel.NewRelationship
			event.Payload = &model.ResolvedNewRelationshipPayload{RelationshipTypeId: classifier, TargetType: strp(unit)}
		case 5:
			event.EventType, refused = esmodel.Alert, true // a measurements payload under another type
		case 6:
			event.EventType = esmodel.EventType(version)
			refused = event.EventType != esmodel.Measurement
		}

		want, wantErr := referenceMarshalResolvedEvent(event)
		got, err := MarshalResolvedEvent(event)
		if (wantErr == nil) != (err == nil) {
			t.Fatalf("proto.Marshal err %v, writer err %v", wantErr, err)
		}
		if err == nil && !bytes.Equal(want, got) {
			t.Fatalf("bytes differ:\n proto.Marshal %x\n writer        %x", want, got)
		}
		if refused && err == nil {
			t.Fatalf("event type %v with a measurements payload was written", event.EventType)
		}
		allValid := true
		for _, s := range []string{source, device, alt, anchor, token, group, value, unit} {
			allValid = allValid && utf8.ValidString(s)
		}
		if wantErr != nil && allValid && !refused {
			t.Fatalf("proto.Marshal refused valid UTF-8: %v", wantErr)
		}
	})
}
