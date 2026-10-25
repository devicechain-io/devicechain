// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
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
// Every instant is written in exactly one of its two forms (eventtime.WireNanos): the count
// of nanoseconds in its proto3 optional sfixed64 field, or, for an instant the count cannot
// hold, the RFC 3339 string in the field every instant was written in before.
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

// Field numbers, from dc-resolved-events.proto.
const (
	resolvedSource               = protowire.Number(1)
	resolvedAltId                = protowire.Number(2)
	resolvedSourceDeviceToken    = protowire.Number(3)
	resolvedAnchors              = protowire.Number(4)
	resolvedOccurredTime         = protowire.Number(7)
	resolvedProcessedTime        = protowire.Number(8)
	resolvedEventType            = protowire.Number(9)
	resolvedPayload              = protowire.Number(10)
	resolvedDeviceTypeToken      = protowire.Number(11)
	resolvedProfileVersionToken  = protowire.Number(12)
	resolvedScopeMemberships     = protowire.Number(13)
	resolvedExternalId           = protowire.Number(14)
	resolvedFenceSetVersion      = protowire.Number(15)
	resolvedSourceDeviceDisabled = protowire.Number(16)
	resolvedOccurred             = protowire.Number(17)
	resolvedProcessed            = protowire.Number(18)
	resolvedGatewayToken         = protowire.Number(19)

	anchorType           = protowire.Number(1)
	anchorToken          = protowire.Number(2)
	anchorRelationshipId = protowire.Number(3)

	membershipGroupToken = protowire.Number(1)
	membershipVersion    = protowire.Number(2)

	measurementsPayloadEntries = protowire.Number(1)
	measurementsEntryReadings  = protowire.Number(1)
	measurementsEntryTime      = protowire.Number(2)
	measurementsEntryNanos     = protowire.Number(3)

	measurementName       = protowire.Number(1)
	measurementValue      = protowire.Number(2)
	measurementClassifier = protowire.Number(3)
	measurementUnit       = protowire.Number(4)
	measurementDataType   = protowire.Number(5)
	measurementDerived    = protowire.Number(6)
)

// tagSize is the size of every tag here but one: a field number up to 15 encodes with its
// wire type in a single byte.
//
// 🔴 resolvedSourceDeviceDisabled (16), resolvedOccurred (17), resolvedProcessed (18) and
// resolvedGatewayToken (19) are the exceptions, sized by disabledSize, envelopeNanosSize and
// gatewayTokenSize: a field number from 16 on takes a TWO-byte tag, and sizing one with
// tagSize would allocate one byte short and refuse every event that carries it.
const tagSize = 1

// entryNanosSize is the encoded size of an entry's time as a count: a one-byte tag and eight
// bytes. envelopeNanosSize is an envelope instant's: its field number takes a two-byte tag.
const (
	entryNanosSize    = tagSize + 8
	envelopeNanosSize = 2 + 8
)

// wireInstant is one instant in the form it is written in: the count (isCount), or the
// string, laid in a scratch buffer from start to end.
type wireInstant struct {
	nanos      int64
	isCount    bool
	start, end int
}

// appendInstant decides the form of at, formatting the string into times when it is the
// string form, and returns the grown scratch.
//
// compact (eventtime.WireCompact) writes the count when it can hold the instant. Otherwise
// it is the string: in UTC where RFC 3339 can carry it there (eventtime.AppendWireString),
// except a LEGACY envelope instant, which is in its own offset, as the release before the
// count formatted it (eventtime.AppendOwnOffsetWireString).
func appendInstant(times []byte, at time.Time, compact, envelope bool) ([]byte, wireInstant) {
	if compact {
		if nanos, ok := eventtime.WireNanos(at); ok {
			return times, wireInstant{nanos: nanos, isCount: true, start: len(times), end: len(times)}
		}
	}
	start := len(times)
	if envelope && !compact {
		times = eventtime.AppendOwnOffsetWireString(times, at)
	} else {
		times = eventtime.AppendWireString(times, at)
	}
	return times, wireInstant{start: start, end: len(times)}
}

// stringSize is the encoded size of the instant's string form at a one-byte-tag field, or
// nothing when it is written as a count.
func (w wireInstant) stringSize() int {
	if w.isCount {
		return 0
	}
	return tagSize + protowire.SizeBytes(w.end-w.start)
}

// countSize is the encoded size of the instant's count at a field whose tag and eight bytes
// take fieldSize (entryNanosSize or envelopeNanosSize), or nothing when it is a string.
func (w wireInstant) countSize(fieldSize int) int {
	if !w.isCount {
		return 0
	}
	return fieldSize
}

// appendString writes the instant's string form at num, or nothing when it is a count.
func (w wireInstant) appendString(out []byte, num protowire.Number, times []byte) []byte {
	if w.isCount {
		return out
	}
	out = protowire.AppendTag(out, num, protowire.BytesType)
	return protowire.AppendBytes(out, times[w.start:w.end])
}

// appendCount writes the instant's count at num, or nothing when it is a string.
func (w wireInstant) appendCount(out []byte, num protowire.Number) []byte {
	if !w.isCount {
		return out
	}
	out = protowire.AppendTag(out, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(out, uint64(w.nanos))
}

// marshalResolvedEvent writes event in the PResolvedEvent wire format into a buffer sized
// exactly once, every instant in format. An unknown format is refused.
func marshalResolvedEvent(event *ResolvedEvent, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	compact := format == eventtime.WireCompact
	// Both envelope times take their form once. A string form, for an instant the count
	// cannot hold, is formatted into a scratch on the stack and copied in.
	var envelopeScratch [96]byte
	times, occurred := appendInstant(envelopeScratch[:0], event.OccurredTime, compact, true)
	times, processed := appendInstant(times, event.ProcessedTime, compact, true)

	// The payload: a measurements payload is sized here and written in place below; any
	// other is marshalled now and copied in.
	var (
		mx            *ResolvedMeasurementsPayload
		mxSizing      measurementsSizing
		entryScratch  [64]byte
		boundsScratch [4]wireInstant
		other         []byte
		payloadSize   int
	)
	if event.EventType == eventtype.Measurement {
		mx, _ = event.Payload.(*ResolvedMeasurementsPayload)
	}
	if mx != nil {
		var err error
		if mxSizing, err = sizeMeasurementsPayload(mx, entryScratch[:0], boundsScratch[:0], compact); err != nil {
			return nil, err
		}
		payloadSize = mxSizing.size
	} else {
		var err error
		if other, err = MarshalResolvedPayload(event.EventType, event.Payload, format); err != nil {
			return nil, err
		}
		payloadSize = len(other)
	}

	size, err := sizeResolvedEnvelope(event, occurred, processed)
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
	out = occurred.appendString(out, resolvedOccurredTime, times)
	out = processed.appendString(out, resolvedProcessedTime, times)
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
	out = appendUint(out, resolvedSourceDeviceDisabled, boolVarint(event.SourceDeviceDisabled))
	out = occurred.appendCount(out, resolvedOccurred)
	out = processed.appendCount(out, resolvedProcessed)
	out = appendString(out, resolvedGatewayToken, event.GatewayToken)

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
// refuses a string proto.Marshal would refuse. The times have already taken their form, and
// a string form is never empty.
func sizeResolvedEnvelope(event *ResolvedEvent, occurred, processed wireInstant) (int, error) {
	size := stringSize(event.Source) + stringSize(event.SourceDeviceToken) +
		stringSize(event.DeviceTypeToken) + stringSize(event.ProfileVersionToken) +
		stringSize(event.ExternalId) + gatewayTokenSize(event.GatewayToken)
	valid := utf8.ValidString(event.Source) && utf8.ValidString(event.SourceDeviceToken) &&
		utf8.ValidString(event.DeviceTypeToken) && utf8.ValidString(event.ProfileVersionToken) &&
		utf8.ValidString(event.ExternalId) && utf8.ValidString(event.GatewayToken)
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
	size += occurred.stringSize() + occurred.countSize(envelopeNanosSize) +
		processed.stringSize() + processed.countSize(envelopeNanosSize)
	size += uintSize(uint64(event.EventType)) + uintSize(uint64(event.FenceSetVersion)) +
		disabledSize(event.SourceDeviceDisabled)
	return size, nil
}

// anchorSize is the encoded size of one PResolvedAnchor's body.
func anchorSize(a *ResolvedAnchor) int {
	return stringSize(a.AnchorType) + stringSize(a.AnchorToken) + uintSize(uint64(a.RelationshipId))
}

// membershipSize is the encoded size of one PScopeMembership's body.
func membershipSize(m *GroupRef) int {
	return stringSize(m.GroupToken) + uintSize(uint64(m.Version))
}

// measurementsSizing is what sizing a measurements payload remembers for writing it: its
// encoded size, the string-form times laid end to end, and each entry's time.
type measurementsSizing struct {
	size    int
	times   []byte
	entries []wireInstant
}

// sizeMeasurementsPayload sizes payload in the PResolvedMeasurementsPayload wire format,
// deciding each entry's time form once (formatting a string form into times), and refuses
// a string proto.Marshal would refuse. times and entries are scratch the caller usually
// keeps on its stack: a one-entry payload, which is what every device sends, needs neither
// to grow.
func sizeMeasurementsPayload(payload *ResolvedMeasurementsPayload, times []byte, entries []wireInstant, compact bool) (measurementsSizing, error) {
	size := 0
	for i := range payload.Entries {
		entry := &payload.Entries[i]
		// Every resolved entry carries its own time, so it is always written
		// (marshalEntryTime in marshal.go), in one form or the other.
		var at wireInstant
		times, at = appendInstant(times, entry.OccurredTime, compact, false)
		entries = append(entries, at)
		body, err := measurementsEntrySize(entry, at)
		if err != nil {
			return measurementsSizing{}, err
		}
		size += tagSize + protowire.SizeBytes(body)
	}
	return measurementsSizing{size: size, times: times, entries: entries}, nil
}

// measurementsEntrySize is the encoded size of one PResolvedMeasurementsEntry's body, whose
// time is at.
func measurementsEntrySize(entry *ResolvedMeasurementsEntry, at wireInstant) (int, error) {
	size := at.stringSize() + at.countSize(entryNanosSize)
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
func measurementValid(mx *ResolvedMeasurementEntry) bool {
	return utf8.ValidString(mx.Name) && utf8.ValidString(mx.Value) &&
		(mx.Unit == nil || utf8.ValidString(*mx.Unit)) &&
		(mx.DataType == nil || utf8.ValidString(*mx.DataType))
}

// measurementSize is the encoded size of one PResolvedMeasurementEntry's body.
func measurementSize(mx *ResolvedMeasurementEntry) int {
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
	if mx.Derived {
		// An implicit-presence bool: written only when true, so a raw entry is the bytes it
		// always was, in either wire format.
		size += tagSize + 1
	}
	return size
}

// appendMeasurementsPayload writes payload's PResolvedMeasurementsPayload body as sized.
func appendMeasurementsPayload(out []byte, payload *ResolvedMeasurementsPayload, sizing measurementsSizing) []byte {
	for i := range payload.Entries {
		entry := &payload.Entries[i]
		at := sizing.entries[i]
		body, _ := measurementsEntrySize(entry, at) // validity was checked when sizing
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
			if mx.Derived {
				out = protowire.AppendTag(out, measurementDerived, protowire.VarintType)
				out = protowire.AppendVarint(out, 1)
			}
		}
		out = at.appendString(out, measurementsEntryTime, sizing.times)
		out = at.appendCount(out, measurementsEntryNanos)
	}
	return out
}

// marshalMeasurementsPayload writes payload alone in the PResolvedMeasurementsPayload wire
// format, into a buffer sized exactly once.
func marshalMeasurementsPayload(payload *ResolvedMeasurementsPayload, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	var entryScratch [64]byte
	var boundsScratch [4]wireInstant
	sizing, err := sizeMeasurementsPayload(payload, entryScratch[:0], boundsScratch[:0], format == eventtime.WireCompact)
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
// boolVarint is a proto3 bool's varint: 1 for true, and 0, which appendUint and uintSize
// leave off the wire as proto.Marshal does, for false.
func boolVarint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// disabledSize is the encoded size of source_device_disabled: nothing when false, and a
// two-byte tag (field 16) plus a one-byte varint when true.
func disabledSize(disabled bool) int {
	if !disabled {
		return 0
	}
	return protowire.SizeTag(resolvedSourceDeviceDisabled) + protowire.SizeVarint(1)
}

// gatewayTokenSize is the encoded size of gateway_token: nothing when empty, and otherwise a
// two-byte tag (field 19) plus the length-prefixed string.
func gatewayTokenSize(token string) int {
	if token == "" {
		return 0
	}
	return protowire.SizeTag(resolvedGatewayToken) + protowire.SizeBytes(len(token))
}

func appendUint(out []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return out
	}
	out = protowire.AppendTag(out, num, protowire.VarintType)
	return protowire.AppendVarint(out, v)
}
