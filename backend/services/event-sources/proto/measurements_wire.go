// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-event-sources/model"
	"google.golang.org/protobuf/encoding/protowire"
)

// The measurements payload is written straight onto the wire rather than through
// proto.Marshal, because its readings are a map<string, string> and the protobuf runtime
// marshals a map field by reflection: a reflect value and an allocation per key and per
// value, ten allocations for a one-reading event. On the HTTP ingest path that made this
// payload the largest single cost of building the inbound message, ahead of the JSON
// decode. Every measurement event an ingest transport publishes passes through here.
//
// The bytes are the PUnresolvedMeasurementsPayload wire format exactly as proto.Marshal
// writes it, field for field:
//
//	payload  entries = 1        repeated message, every entry written, an empty one too
//	entry    measurements = 1   map<string,string>, one length-delimited item per reading,
//	                            the item's key (1) and value (2) ALWAYS written, empty or not
//	         occurred_time = 2  proto3 optional string, written when set, empty or not
//
// so proto.Unmarshal reads back the same message (measurements_wire_test.go holds this
// against proto.Marshal itself). The readings come out in map iteration order, which is
// what proto.Marshal does too unless asked to be deterministic, and nothing downstream
// reads the bytes as anything but a message.
//
// 🔴 A field added to either message in the .proto must be added here too, or it never
// reaches the wire. measurements_wire_shape_test.go populates every field the generated
// descriptor declares, sends it through the consumer's decoder and this writer, and fails,
// naming the field, until both carry it.
//
// 🔴 A reading name or value that is not valid UTF-8 is REFUSED, as proto.Marshal refuses
// it for a proto3 string. The receiving side would refuse the message anyway, and refusing
// it here keeps that a publish-time error rather than a poisoned message in the stream.

// errMeasurementsNotUTF8 is the refusal of a reading whose name or value is not valid UTF-8.
var errMeasurementsNotUTF8 = errors.New("measurement name or value is not valid UTF-8")

// Field numbers and the one wire type every field here uses.
const (
	measurementsPayloadEntries = protowire.Number(1)
	measurementsEntryReadings  = protowire.Number(1)
	measurementsEntryTime      = protowire.Number(2)
	mapItemKey                 = protowire.Number(1)
	mapItemValue               = protowire.Number(2)
)

// tagSize is the size of every tag here: field numbers 1 and 2 with the bytes wire type
// encode in a single byte.
const tagSize = 1

// marshalMeasurementsPayload writes payload in the PUnresolvedMeasurementsPayload wire
// format into a buffer sized exactly once.
//
// It sizes the message first and then writes it, so each entry's time is formatted once,
// into a scratch buffer both passes share, and each entry's size is remembered for the
// second pass. A one-entry payload — what every device sends — needs neither to grow.
func marshalMeasurementsPayload(payload *model.UnresolvedMeasurementsPayload) ([]byte, error) {
	var timeScratch [64]byte
	times := timeScratch[:0]
	var boundsScratch [4]entryBounds
	bounds := boundsScratch[:0]

	size := 0
	for _, entry := range payload.Entries {
		b := entryBounds{timeStart: len(times), timeEnd: -1}
		for name, value := range entry.Measurements {
			if !utf8.ValidString(name) || !utf8.ValidString(value) {
				return nil, errMeasurementsNotUTF8
			}
			b.size += tagSize + protowire.SizeBytes(mapItemSize(name, value))
		}
		if entry.OccurredTime != nil {
			times = entry.OccurredTime.UTC().AppendFormat(times, time.RFC3339Nano)
			b.timeEnd = len(times)
			b.size += tagSize + protowire.SizeBytes(b.timeEnd-b.timeStart)
		}
		bounds = append(bounds, b)
		size += tagSize + protowire.SizeBytes(b.size)
	}

	out := make([]byte, 0, size)
	for i, entry := range payload.Entries {
		b := bounds[i]
		out = protowire.AppendTag(out, measurementsPayloadEntries, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(b.size))
		for name, value := range entry.Measurements {
			out = protowire.AppendTag(out, measurementsEntryReadings, protowire.BytesType)
			out = protowire.AppendVarint(out, uint64(mapItemSize(name, value)))
			out = protowire.AppendTag(out, mapItemKey, protowire.BytesType)
			out = protowire.AppendString(out, name)
			out = protowire.AppendTag(out, mapItemValue, protowire.BytesType)
			out = protowire.AppendString(out, value)
		}
		if b.timeEnd >= 0 {
			out = protowire.AppendTag(out, measurementsEntryTime, protowire.BytesType)
			out = protowire.AppendBytes(out, times[b.timeStart:b.timeEnd])
		}
	}
	// The two passes walk the same maps, so they agree unless a map changed between them,
	// which only a caller mutating the payload concurrently could do. That is a defect, and
	// a message whose length prefixes disagree with its contents must not be published.
	if len(out) != size {
		return nil, fmt.Errorf("measurements payload changed while it was being marshalled: sized %d bytes, wrote %d",
			size, len(out))
	}
	return out, nil
}

// entryBounds is what the sizing pass remembers about one entry for the writing pass: its
// encoded size, and where its formatted time sits in the shared scratch (timeEnd -1: none).
type entryBounds struct {
	size               int
	timeStart, timeEnd int
}

// mapItemSize is the encoded size of one map item's body: the key and the value, each
// always written.
func mapItemSize(name, value string) int {
	return tagSize + protowire.SizeBytes(len(name)) + tagSize + protowire.SizeBytes(len(value))
}
