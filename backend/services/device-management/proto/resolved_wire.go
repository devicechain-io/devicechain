// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"google.golang.org/protobuf/encoding/protowire"
)

// The resolved event is written straight onto the wire rather than through proto.Marshal.
// Every event device-management resolves is published through here, once, and building
// the generated message graph for it was about a fifth of device-management's in-process
// work per event: one struct per reading, per entry, per anchor and per membership, a
// pointer for each optional string, a formatted string per time, and the payload marshalled
// into a buffer of its own only to be copied into the envelope's. Written directly, the
// event is sized once and written once, into one buffer, and a measurements payload is
// written in place inside it.
//
// The bytes are the PResolvedEvent wire format exactly as proto.Marshal writes it, field
// for field and in field-number order, which is the order the runtime writes a generated
// message in. No field here is a map, so there is no iteration order to differ: the output
// is byte-identical to proto.Marshal's for every input (resolved_wire_test.go holds it
// against proto.Marshal itself, including under fuzzing). The proto3 rules applied:
//
//	implicit scalar or string   written only when it is not the zero value
//	proto3 optional             written whenever it is set, zero value or not
//	repeated message            every element written, an empty one as a zero-length item
//	int32 / int64               a negative value sign-extended to a ten-byte varint
//
// Only the measurements payload is written in place. Every other payload is marshalled as
// before (MarshalResolvedPayload) and copied in: those events are not the ingest hot path.
//
// 🔴 A field added to PResolvedEvent, PResolvedAnchor, PScopeMembership,
// PResolvedMeasurementsPayload, PResolvedMeasurementsEntry or PResolvedMeasurementEntry
// must be added here too, or it never reaches the wire. resolved_wire_shape_test.go
// populates every field the generated descriptor declares, sends it through the consumer's
// decoder and this writer, and fails, naming the field, until both carry it.
//
// 🔴 A string that is not valid UTF-8 is REFUSED, as proto.Marshal refuses it for a proto3
// string. Every consumer would refuse the message anyway, and refusing it here keeps that a
// publish-time error rather than a poisoned message in the stream.

// errResolvedNotUTF8 is the refusal of an event carrying a string that is not valid UTF-8.
var errResolvedNotUTF8 = errors.New("resolved event carries a string that is not valid UTF-8")

// Field numbers, from dc-device-management-events.proto.
const (
	resolvedSource              = protowire.Number(1)
	resolvedAltId               = protowire.Number(2)
	resolvedSourceDeviceToken   = protowire.Number(3)
	resolvedAnchors             = protowire.Number(4)
	resolvedOccurredTime        = protowire.Number(7)
	resolvedProcessedTime       = protowire.Number(8)
	resolvedEventType           = protowire.Number(9)
	resolvedPayload             = protowire.Number(10)
	resolvedDeviceTypeToken     = protowire.Number(11)
	resolvedProfileVersionToken = protowire.Number(12)
	resolvedScopeMemberships    = protowire.Number(13)
	resolvedExternalId          = protowire.Number(14)
	resolvedFenceSetVersion     = protowire.Number(15)

	anchorType           = protowire.Number(1)
	anchorToken          = protowire.Number(2)
	anchorRelationshipId = protowire.Number(3)

	membershipGroupToken = protowire.Number(1)
	membershipVersion    = protowire.Number(2)

	measurementsPayloadEntries = protowire.Number(1)
	measurementsEntryReadings  = protowire.Number(1)
	measurementsEntryTime      = protowire.Number(2)

	measurementName       = protowire.Number(1)
	measurementValue      = protowire.Number(2)
	measurementClassifier = protowire.Number(3)
	measurementUnit       = protowire.Number(4)
	measurementDataType   = protowire.Number(5)
)

// tagSize is the size of every tag here: every field number is at most 15, and a field
// number up to 15 encodes with its wire type in a single byte.
const tagSize = 1

// marshalResolvedEvent writes event in the PResolvedEvent wire format into a buffer sized
// exactly once.
func marshalResolvedEvent(event *model.ResolvedEvent) ([]byte, error) {
	// Both envelope times are formatted once, into a scratch on the stack, and copied in.
	var envelopeScratch [96]byte
	times := event.OccurredTime.AppendFormat(envelopeScratch[:0], time.RFC3339Nano)
	occurredEnd := len(times)
	times = event.ProcessedTime.AppendFormat(times, time.RFC3339Nano)

	// The payload: a measurements payload is sized here and written in place below; any
	// other is marshalled now and copied in.
	var (
		mx            *model.ResolvedMeasurementsPayload
		mxSizing      measurementsSizing
		entryScratch  [64]byte
		boundsScratch [4]int
		other         []byte
		payloadSize   int
	)
	if event.EventType == esmodel.Measurement {
		mx, _ = event.Payload.(*model.ResolvedMeasurementsPayload)
	}
	if mx != nil {
		var err error
		if mxSizing, err = sizeMeasurementsPayload(mx, entryScratch[:0], boundsScratch[:0]); err != nil {
			return nil, err
		}
		payloadSize = mxSizing.size
	} else {
		var err error
		if other, err = MarshalResolvedPayload(event.EventType, event.Payload); err != nil {
			return nil, err
		}
		payloadSize = len(other)
	}

	size, err := sizeResolvedEnvelope(event, occurredEnd, len(times)-occurredEnd)
	if err != nil {
		return nil, err
	}
	if payloadSize > 0 {
		size += tagSize + protowire.SizeBytes(payloadSize)
	}

	out := make([]byte, 0, size)
	out = appendString(out, resolvedSource, event.Source)
	if event.AltId != nil {
		out = appendPresentString(out, resolvedAltId, *event.AltId)
	}
	out = appendString(out, resolvedSourceDeviceToken, event.SourceDeviceToken)
	for i := range event.Anchors {
		a := &event.Anchors[i]
		out = protowire.AppendTag(out, resolvedAnchors, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(anchorSize(a)))
		out = appendString(out, anchorType, a.AnchorType)
		out = appendString(out, anchorToken, a.AnchorToken)
		out = appendUint(out, anchorRelationshipId, uint64(a.RelationshipId))
	}
	out = protowire.AppendTag(out, resolvedOccurredTime, protowire.BytesType)
	out = protowire.AppendBytes(out, times[:occurredEnd])
	out = protowire.AppendTag(out, resolvedProcessedTime, protowire.BytesType)
	out = protowire.AppendBytes(out, times[occurredEnd:])
	out = appendUint(out, resolvedEventType, uint64(event.EventType))
	if payloadSize > 0 {
		out = protowire.AppendTag(out, resolvedPayload, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(payloadSize))
		if mx != nil {
			out = appendMeasurementsPayload(out, mx, mxSizing)
		} else {
			out = append(out, other...)
		}
	}
	// The three proto3 optional tokens: an empty one is written as ABSENT, never as a
	// present empty string, so a device with no resolvable profile version (or type, or
	// external id) reads back as the empty token through the generated Get* accessor.
	out = appendString(out, resolvedDeviceTypeToken, event.DeviceTypeToken)
	out = appendString(out, resolvedProfileVersionToken, event.ProfileVersionToken)
	for i := range event.ScopeMemberships {
		m := &event.ScopeMemberships[i]
		out = protowire.AppendTag(out, resolvedScopeMemberships, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(membershipSize(m)))
		out = appendString(out, membershipGroupToken, m.GroupToken)
		out = appendUint(out, membershipVersion, uint64(m.Version))
	}
	out = appendString(out, resolvedExternalId, event.ExternalId)
	out = appendUint(out, resolvedFenceSetVersion, uint64(event.FenceSetVersion))

	// The sizing and writing passes read the same event, so they agree unless it changed
	// between them, which only a caller mutating it concurrently could do. That is a
	// defect, and a message whose length prefixes disagree with its contents must not be
	// published.
	if len(out) != size {
		return nil, fmt.Errorf("resolved event changed while it was being marshalled: sized %d bytes, wrote %d",
			size, len(out))
	}
	return out, nil
}

// sizeResolvedEnvelope is the encoded size of every envelope field but the payload, and
// refuses a string proto.Marshal would refuse. The times are already formatted and are
// never empty.
func sizeResolvedEnvelope(event *model.ResolvedEvent, occurredLen, processedLen int) (int, error) {
	size := stringSize(event.Source) + stringSize(event.SourceDeviceToken) +
		stringSize(event.DeviceTypeToken) + stringSize(event.ProfileVersionToken) +
		stringSize(event.ExternalId)
	valid := utf8.ValidString(event.Source) && utf8.ValidString(event.SourceDeviceToken) &&
		utf8.ValidString(event.DeviceTypeToken) && utf8.ValidString(event.ProfileVersionToken) &&
		utf8.ValidString(event.ExternalId)
	if event.AltId != nil {
		size += presentStringSize(*event.AltId)
		valid = valid && utf8.ValidString(*event.AltId)
	}
	for i := range event.Anchors {
		a := &event.Anchors[i]
		size += tagSize + protowire.SizeBytes(anchorSize(a))
		valid = valid && utf8.ValidString(a.AnchorType) && utf8.ValidString(a.AnchorToken)
	}
	for i := range event.ScopeMemberships {
		m := &event.ScopeMemberships[i]
		size += tagSize + protowire.SizeBytes(membershipSize(m))
		valid = valid && utf8.ValidString(m.GroupToken)
	}
	if !valid {
		return 0, errResolvedNotUTF8
	}
	size += tagSize + protowire.SizeBytes(occurredLen) + tagSize + protowire.SizeBytes(processedLen)
	size += uintSize(uint64(event.EventType)) + uintSize(uint64(event.FenceSetVersion))
	return size, nil
}

// anchorSize is the encoded size of one PResolvedAnchor's body.
func anchorSize(a *model.ResolvedAnchor) int {
	return stringSize(a.AnchorType) + stringSize(a.AnchorToken) + uintSize(uint64(a.RelationshipId))
}

// membershipSize is the encoded size of one PScopeMembership's body.
func membershipSize(m *model.GroupRef) int {
	return stringSize(m.GroupToken) + uintSize(uint64(m.Version))
}

// measurementsSizing is what sizing a measurements payload remembers for writing it: its
// encoded size, every entry's formatted time laid end to end, and where each entry's time
// ends in that buffer (an entry's time starts where the previous one's ends).
type measurementsSizing struct {
	size     int
	times    []byte
	timeEnds []int
}

// sizeMeasurementsPayload sizes payload in the PResolvedMeasurementsPayload wire format,
// formatting each entry's time once into times, and refuses a string proto.Marshal would
// refuse. times and timeEnds are scratch the caller usually keeps on its stack: a
// one-entry payload, which is what every device sends, needs neither to grow.
func sizeMeasurementsPayload(payload *model.ResolvedMeasurementsPayload, times []byte, timeEnds []int) (measurementsSizing, error) {
	size := 0
	for i := range payload.Entries {
		entry := &payload.Entries[i]
		start := len(times)
		// Every resolved entry carries its own time, so it is always written
		// (marshalEntryTime in marshal.go), in UTC.
		times = entry.OccurredTime.UTC().AppendFormat(times, time.RFC3339Nano)
		timeEnds = append(timeEnds, len(times))
		body, err := measurementsEntrySize(entry, len(times)-start)
		if err != nil {
			return measurementsSizing{}, err
		}
		size += tagSize + protowire.SizeBytes(body)
	}
	return measurementsSizing{size: size, times: times, timeEnds: timeEnds}, nil
}

// measurementsEntrySize is the encoded size of one PResolvedMeasurementsEntry's body,
// whose time formats to timeLen bytes.
func measurementsEntrySize(entry *model.ResolvedMeasurementsEntry, timeLen int) (int, error) {
	size := tagSize + protowire.SizeBytes(timeLen)
	for j := range entry.Entries {
		mx := &entry.Entries[j]
		if !measurementValid(mx) {
			return 0, errResolvedNotUTF8
		}
		size += tagSize + protowire.SizeBytes(measurementSize(mx))
	}
	return size, nil
}

// measurementValid reports whether every string of one reading is valid UTF-8.
func measurementValid(mx *model.ResolvedMeasurementEntry) bool {
	return utf8.ValidString(mx.Name) && utf8.ValidString(mx.Value) &&
		(mx.Unit == nil || utf8.ValidString(*mx.Unit)) &&
		(mx.DataType == nil || utf8.ValidString(*mx.DataType))
}

// measurementSize is the encoded size of one PResolvedMeasurementEntry's body.
func measurementSize(mx *model.ResolvedMeasurementEntry) int {
	size := stringSize(mx.Name) + stringSize(mx.Value)
	if mx.Classifier != nil {
		size += tagSize + protowire.SizeVarint(*mx.Classifier)
	}
	if mx.Unit != nil {
		size += presentStringSize(*mx.Unit)
	}
	if mx.DataType != nil {
		size += presentStringSize(*mx.DataType)
	}
	return size
}

// appendMeasurementsPayload writes payload's PResolvedMeasurementsPayload body as sized.
func appendMeasurementsPayload(out []byte, payload *model.ResolvedMeasurementsPayload, sizing measurementsSizing) []byte {
	start := 0
	for i := range payload.Entries {
		entry := &payload.Entries[i]
		end := sizing.timeEnds[i]
		body, _ := measurementsEntrySize(entry, end-start) // validity was checked when sizing
		out = protowire.AppendTag(out, measurementsPayloadEntries, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(body))
		for j := range entry.Entries {
			mx := &entry.Entries[j]
			out = protowire.AppendTag(out, measurementsEntryReadings, protowire.BytesType)
			out = protowire.AppendVarint(out, uint64(measurementSize(mx)))
			out = appendString(out, measurementName, mx.Name)
			out = appendString(out, measurementValue, mx.Value)
			if mx.Classifier != nil {
				out = protowire.AppendTag(out, measurementClassifier, protowire.VarintType)
				out = protowire.AppendVarint(out, *mx.Classifier)
			}
			if mx.Unit != nil {
				out = appendPresentString(out, measurementUnit, *mx.Unit)
			}
			if mx.DataType != nil {
				out = appendPresentString(out, measurementDataType, *mx.DataType)
			}
		}
		out = protowire.AppendTag(out, measurementsEntryTime, protowire.BytesType)
		out = protowire.AppendBytes(out, sizing.times[start:end])
		start = end
	}
	return out
}

// marshalMeasurementsPayload writes payload alone in the PResolvedMeasurementsPayload wire
// format, into a buffer sized exactly once.
func marshalMeasurementsPayload(payload *model.ResolvedMeasurementsPayload) ([]byte, error) {
	var entryScratch [64]byte
	var boundsScratch [4]int
	sizing, err := sizeMeasurementsPayload(payload, entryScratch[:0], boundsScratch[:0])
	if err != nil {
		return nil, err
	}
	out := appendMeasurementsPayload(make([]byte, 0, sizing.size), payload, sizing)
	if len(out) != sizing.size {
		return nil, fmt.Errorf("measurements payload changed while it was being marshalled: sized %d bytes, wrote %d",
			sizing.size, len(out))
	}
	return out, nil
}

// stringSize is the encoded size of an implicit-presence proto3 string: nothing when empty.
func stringSize(s string) int {
	if s == "" {
		return 0
	}
	return presentStringSize(s)
}

// presentStringSize is the encoded size of a string field that is written, empty or not.
func presentStringSize(s string) int {
	return tagSize + protowire.SizeBytes(len(s))
}

// uintSize is the encoded size of an implicit-presence varint field: nothing when zero. A
// signed value arrives sign-extended to 64 bits, as the runtime writes it.
func uintSize(v uint64) int {
	if v == 0 {
		return 0
	}
	return tagSize + protowire.SizeVarint(v)
}

// appendString writes an implicit-presence proto3 string: nothing when empty.
func appendString(out []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return out
	}
	return appendPresentString(out, num, s)
}

// appendPresentString writes a string field, empty or not.
func appendPresentString(out []byte, num protowire.Number, s string) []byte {
	out = protowire.AppendTag(out, num, protowire.BytesType)
	return protowire.AppendString(out, s)
}

// appendUint writes an implicit-presence varint field: nothing when zero.
func appendUint(out []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return out
	}
	out = protowire.AppendTag(out, num, protowire.VarintType)
	return protowire.AppendVarint(out, v)
}
