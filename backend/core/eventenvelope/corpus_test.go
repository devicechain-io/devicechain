// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"fmt"
	"hash/crc32"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func sp(s string) *string { return &s }
func up(v uint64) *uint64 { return &v }

var t0 = time.Date(2026, 11, 3, 9, 30, 15, 123456000, time.UTC)

// loadtestResolved is the audit's "measurement, 1 metric" resolved event, the one its 138 B
// per event and 636 B per 16 were measured on: a fleet's devices of one type, each reporting
// one reading with its unit and data type, a few milliseconds apart, no anchors. This is the
// shape the size gate is measured on.
func loadtestResolved(i int) *resolvedevent.ResolvedEvent {
	at := t0.Add(time.Duration(i) * 3 * time.Millisecond)
	return &resolvedevent.ResolvedEvent{
		Source:              "mqtt",
		SourceDeviceToken:   fmt.Sprintf("sim-%06d", 41000+i*13),
		DeviceTypeToken:     "thermostat",
		ProfileVersionToken: "thermostat-profile@3",
		OccurredTime:        at,
		ProcessedTime:       at.Add(900 * time.Microsecond),
		EventType:           eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries: []resolvedevent.ResolvedMeasurementEntry{
				{Name: "temperature", Value: fmt.Sprintf("%d.%d", 18+i%9, (i*7)%10), Unit: sp("Cel"), DataType: sp("DOUBLE")},
			},
		}}},
	}
}

// realisticResolved is a heavier event of a mixed telemetry fleet: devices of a few types,
// each reporting a sample of three readings with units, one customer anchor.
func realisticResolved(i int) *resolvedevent.ResolvedEvent {
	at := t0.Add(time.Duration(i) * 3 * time.Millisecond)
	kinds := []string{"thermostat", "pump-controller", "door-sensor"}
	kind := kinds[i%len(kinds)]
	return &resolvedevent.ResolvedEvent{
		Source:              "mqtt",
		SourceDeviceToken:   fmt.Sprintf("%s-%04d", kind, 1000+i*7),
		DeviceTypeToken:     kind,
		ProfileVersionToken: kind + "-profile@3",
		Anchors:             []resolvedevent.ResolvedAnchor{{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 7}},
		OccurredTime:        at,
		ProcessedTime:       at.Add(900 * time.Microsecond),
		EventType:           eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries: []resolvedevent.ResolvedMeasurementEntry{
				{Name: "temperature", Value: fmt.Sprintf("%d.%d", 20+i%5, i%10), Unit: sp("Cel"), DataType: sp("DOUBLE")},
				{Name: "humidity", Value: fmt.Sprintf("%d.5", 40+i%20), Unit: sp("%"), DataType: sp("DOUBLE")},
				{Name: "battery", Value: fmt.Sprintf("%d", 100-i), Unit: sp("%"), DataType: sp("INT")},
			},
		}}},
	}
}

// legacyBytes is what device-management publishes for e today.
func legacyBytes(t testing.TB, e *resolvedevent.ResolvedEvent, f eventtime.WireFormat) []byte {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEventAs(e, f)
	require.NoError(t, err)
	return b
}

func corpus(t testing.TB, n int) []eventenvelope.Event {
	t.Helper()
	return corpusOf(t, n, realisticResolved)
}

func corpusOf(t testing.TB, n int, gen func(int) *resolvedevent.ResolvedEvent) []eventenvelope.Event {
	t.Helper()
	out := make([]eventenvelope.Event, n)
	for i := range out {
		out[i] = eventenvelope.Event{Index: uint32(i), Bytes: legacyBytes(t, gen(i), eventtime.WireCompact)}
	}
	return out
}

func resolvedHeader() eventenvelope.Header {
	return eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme", SourceSeq: 4242}
}

// unresolvedBytes writes a PUnresolvedEvent the way event-sources' marshaller does, field by
// field in field-number order: this module cannot import that one, and the envelope works on
// the bytes alone.
func unresolvedBytes(source, device string, typ int64, payload []byte, occurred, processed *int64) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, source)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, device)
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(typ))
	b = protowire.AppendTag(b, 8, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	if occurred != nil {
		b = protowire.AppendTag(b, 14, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, uint64(*occurred))
	}
	if processed != nil {
		b = protowire.AppendTag(b, 15, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, uint64(*processed))
	}
	return b
}

// seal appends the checksum field to a hand-built message body, so a test can build a message
// that is well-formed except for the one thing it is about.
func seal(body []byte) []byte {
	out := append([]byte(nil), body...)
	out = protowire.AppendTag(out, 13, protowire.Fixed32Type)
	return protowire.AppendFixed32(out, crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
}
