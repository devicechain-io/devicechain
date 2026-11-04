// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventenvelope is the wire format that lets one NATS message on inbound-events or
// resolved-events carry k events of one tenant instead of one.
//
// An envelope is a header (version, kind, tenant, source coordinate, settlement round, base
// time, a small dictionary of interned tokens) and k members. A member is today's
// PUnresolvedEvent / PResolvedEvent bytes with the fields that are constant across the
// envelope taken out: the device-type and profile-version tokens (named through the
// dictionary) and the two instants (written as deltas against the base time). The package
// works on those bytes and nothing else, so it carries no knowledge of either event type
// beyond which field numbers are interned (layoutFor), and the existing encoders and decoders
// are reused unchanged: Encode takes the bytes MarshalResolvedEvent produced, and a reader
// gets the same bytes back from Reader.Member, byte for byte for every event the hand-written
// encoders write (they write fields in field-number order, and so does the rebuild).
//
// A message is an envelope exactly when it carries the Dc-Env header (Marked). A message
// without it is a single legacy event and stays valid input forever; an envelope of one
// event is never produced (ErrSingleMember), so a tenant that sends one event at a time sees
// no format change.
//
// Bounds, enforced on both sides and refused loudly: at most MaxMembers members, at most
// MaxRawBytes of member bytes before compression. Above CompressMin raw bytes the members
// are one s2 block; the length is declared up front and checked before anything is inflated,
// so a small message cannot expand into a large allocation.
//
// The schema is dc-event-envelope.proto. Nothing is generated from it: encode.go and
// decode.go write and read it directly.
package eventenvelope

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// HeaderName is the NATS header that marks a message as an envelope. A reader dispatches
	// on it without touching the payload.
	HeaderName = "Dc-Env"
	// HeaderValue is the value of HeaderName for this format version.
	HeaderValue = "1"

	// Version is the format version written to, and required of, the header field.
	Version = 1

	// MaxMembers bounds k.
	MaxMembers = 64
	// MaxRawBytes bounds the member bytes of one envelope before compression. 64 KiB keeps a
	// raft append entry and a filestore block write small (the streams' MaxMsgSize is 1 MiB).
	// An event that alone exceeds it travels alone, in the legacy shape.
	MaxRawBytes = 64 << 10
	// CompressMin is the raw size above which the members are compressed: below it s2 gains
	// nothing.
	CompressMin = 512

	// maxDictEntries and maxDictEntryBytes bound the dictionary. Two refs per member is the
	// most a message can name, so MaxMembers*2 entries is the most it can need.
	maxDictEntries    = MaxMembers * 2
	maxDictEntryBytes = 1024
	// maxIndex bounds a member's source index, so a corrupt one cannot be a billion.
	maxIndex = 1 << 20
	// maxMessageBytes bounds the message itself: the members are at most MaxRawBytes, the
	// header and dictionary are small and bounded above.
	maxMessageBytes = MaxRawBytes + 2*maxDictEntries*maxDictEntryBytes/8 + 4096
)

// Kind says which stream an envelope's events belong to.
type Kind uint8

const (
	// KindUnresolved is PUnresolvedEvent members, for inbound-events.
	KindUnresolved Kind = 1
	// KindResolved is PResolvedEvent members, for resolved-events.
	KindResolved Kind = 2
)

func (k Kind) String() string {
	switch k {
	case KindUnresolved:
		return "unresolved"
	case KindResolved:
		return "resolved"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

func (k Kind) valid() bool { return k == KindUnresolved || k == KindResolved }

// The compression codecs. Values are the wire values.
const (
	codecNone = 0
	codecS2   = 1
)

// Errors. Every refusal wraps one, so a caller can tell a malformed message (poison: it will
// never decode) from a refused request to encode.
var (
	// ErrTooManyMembers refuses an envelope of more than MaxMembers.
	ErrTooManyMembers = errors.New("event envelope: more than 64 members")
	// ErrTooLarge refuses an envelope beyond its size bounds: more than MaxRawBytes of member
	// bytes, or a whole message longer than any envelope within them can be.
	ErrTooLarge = errors.New("event envelope: exceeds the size bound (64 KiB of members)")
	// ErrSingleMember refuses to envelope exactly one event: that is the legacy shape.
	ErrSingleMember = errors.New("event envelope: one event is published in the legacy shape")
	// ErrEmpty refuses an envelope that carries nothing at all.
	ErrEmpty = errors.New("event envelope: no members and no settled indices")
	// ErrBadInput refuses a request to encode: a bad kind, tenant, index order, or an event
	// that is not well-formed protobuf or carries an interned field in a form the format
	// cannot hold.
	ErrBadInput = errors.New("event envelope: invalid input")
	// ErrCorrupt marks a message that is not a valid envelope: a bad checksum, framing, a
	// count or size that disagrees with what was decoded, or a field outside the format.
	ErrCorrupt = errors.New("event envelope: corrupt")
	// ErrVersion marks an envelope of a version this reader does not know.
	ErrVersion = errors.New("event envelope: unknown version")
	// ErrTenant marks an envelope whose tenant is not the subject's.
	ErrTenant = errors.New("event envelope: tenant does not match the subject")
)

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

func badInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadInput, fmt.Sprintf(format, args...))
}

// Header is what an envelope says about its members as a whole.
type Header struct {
	Kind Kind
	// Tenant must be the tenant of every member and of the subject the envelope is published
	// to; a reader checks the second (Reader.VerifyTenant).
	Tenant string
	// SourceSeq is the coordinate the envelope derives from: for a resolved envelope the
	// inbound envelope's stream sequence, for an inbound one the first capture sequence (0
	// when there is none).
	SourceSeq uint64
	// Round is the settlement round, 0 normally.
	Round uint32
	// SettledElsewhere lists the source indices of members settled outside this message
	// (recorded as failed events or split out), ascending and disjoint from the members'.
	SettledElsewhere []uint32
}

// Event is one member to encode: the full bytes of a PUnresolvedEvent or PResolvedEvent
// exactly as its marshaller wrote them, and the member's position in the source.
type Event struct {
	Index uint32
	Bytes []byte
}

// Marked reports whether a message's headers mark it as an envelope of the format this package
// reads: the header is present AND names this version. h is a nats.Header, or anything with its
// Get. A message marked with another value is not Marked, so a reader treats it as what it
// cannot decode (poison) instead of guessing at a format it does not know.
func Marked(h interface{ Get(string) string }) bool {
	return h != nil && h.Get(HeaderName) == HeaderValue
}

// layout names the field numbers of an event that the envelope interns. 0 = none.
type layout struct {
	refA, refB          protowire.Number // device-type and profile-version tokens
	occurred, processed protowire.Number // the sfixed64 instants
}

var (
	// PResolvedEvent: device_type_token 11, profile_version_token 12, occurred 17, processed 18.
	resolvedLayout = layout{refA: 11, refB: 12, occurred: 17, processed: 18}
	// PUnresolvedEvent: occurred 14, processed 15. It has no per-tenant strings to intern.
	unresolvedLayout = layout{occurred: 14, processed: 15}
)

func layoutFor(k Kind) *layout {
	if k == KindResolved {
		return &resolvedLayout
	}
	return &unresolvedLayout
}

// Member flag bits.
const (
	flagNoOccurred  = 1 << 0
	flagNoProcessed = 1 << 1
	flagMask        = flagNoOccurred | flagNoProcessed
)

// parts is one event once its interned fields are taken out.
type parts struct {
	event           []byte // the event bytes without the interned fields
	refA, refB      uint32 // dictionary index + 1, 0 = the event had no such field
	occurred        int64
	processed       int64
	hasOcc, hasProc bool
}

// strip copies the fields of event that are not interned onto dst and returns it with what
// it took out. Interned strings are returned as slices of event, for the caller to intern.
//
// 🔴 strip REFUSES (ErrBadInput) an event that Member(i) would not give back byte for byte, so
// "a reader gets the producer's bytes" is a property of Encode and not an assumption about its
// callers. The rebuild puts the interned fields back in field-number order with the shortest tag
// and length, so an event is refused when
//
//   - its fields are not in ascending field-number order (equal numbers, a repeated
//     non-interned field, are fine): proto.Marshal writes unknown fields last, after field 18, and
//     an event with one would come back with 17 and 18 after it;
//   - an interned field's tag or length is not the shortest encoding of itself.
//
// The producer falls back to the legacy shape for such an event, which is always valid.
func strip(dst []byte, event []byte, lay *layout) (out []byte, p parts, a, b []byte, hasA, hasB bool, err error) {
	out = dst
	rest := event
	var last protowire.Number
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return dst, p, nil, nil, false, false, badInput("event is not well-formed protobuf")
		}
		vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if vn < 0 {
			return dst, p, nil, nil, false, false, badInput("event is not well-formed protobuf")
		}
		if num < last {
			return dst, p, nil, nil, false, false, badInput("event field %d follows field %d: fields are not in ascending order", num, last)
		}
		last = num
		interned := (lay.refA != 0 && num == lay.refA) || (lay.refB != 0 && num == lay.refB) ||
			num == lay.occurred || num == lay.processed
		if interned && n != protowire.SizeVarint(protowire.EncodeTag(num, typ)) {
			return dst, p, nil, nil, false, false, badInput("event field %d has a tag that is not the shortest encoding", num)
		}
		field := rest[:n+vn]
		switch {
		case lay.refA != 0 && num == lay.refA:
			if typ != protowire.BytesType || hasA {
				return dst, p, nil, nil, false, false, badInput("event field %d is repeated or not a string", num)
			}
			a, _ = protowire.ConsumeBytes(rest[n:])
			if vn-len(a) != protowire.SizeVarint(uint64(len(a))) {
				return dst, p, nil, nil, false, false, badInput("event field %d has a length that is not the shortest encoding", num)
			}
			hasA = true
		case lay.refB != 0 && num == lay.refB:
			if typ != protowire.BytesType || hasB {
				return dst, p, nil, nil, false, false, badInput("event field %d is repeated or not a string", num)
			}
			b, _ = protowire.ConsumeBytes(rest[n:])
			if vn-len(b) != protowire.SizeVarint(uint64(len(b))) {
				return dst, p, nil, nil, false, false, badInput("event field %d has a length that is not the shortest encoding", num)
			}
			hasB = true
		case num == lay.occurred:
			if typ != protowire.Fixed64Type || p.hasOcc {
				return dst, p, nil, nil, false, false, badInput("event field %d is repeated or not an instant", num)
			}
			v, _ := protowire.ConsumeFixed64(rest[n:])
			p.occurred, p.hasOcc = int64(v), true
		case num == lay.processed:
			if typ != protowire.Fixed64Type || p.hasProc {
				return dst, p, nil, nil, false, false, badInput("event field %d is repeated or not an instant", num)
			}
			v, _ := protowire.ConsumeFixed64(rest[n:])
			p.processed, p.hasProc = int64(v), true
		default:
			out = append(out, field...)
		}
		rest = rest[n+vn:]
	}
	return out, p, a, b, hasA, hasB, nil
}

// rebuild appends to dst the event that stripped came from: the fields of stripped with the
// interned ones put back where field-number order puts them. For an event written in field
// order the result is byte-identical to the original.
func rebuild(dst, stripped []byte, lay *layout, a, b string, hasA, hasB bool, occurred, processed int64, hasOcc, hasProc bool) ([]byte, error) {
	type inject struct {
		num protowire.Number
		is  byte // 's' string, 'i' instant
		str string
		val int64
	}
	var pending [4]inject
	np := 0
	if hasA {
		pending[np] = inject{num: lay.refA, is: 's', str: a}
		np++
	}
	if hasB {
		pending[np] = inject{num: lay.refB, is: 's', str: b}
		np++
	}
	if hasOcc {
		pending[np] = inject{num: lay.occurred, is: 'i', val: occurred}
		np++
	}
	if hasProc {
		pending[np] = inject{num: lay.processed, is: 'i', val: processed}
		np++
	}
	// The injections are already in ascending field order: refA < refB < occurred < processed
	// holds for both layouts.
	emit := func(in inject) {
		if in.is == 's' {
			dst = protowire.AppendTag(dst, in.num, protowire.BytesType)
			dst = protowire.AppendString(dst, in.str)
			return
		}
		dst = protowire.AppendTag(dst, in.num, protowire.Fixed64Type)
		dst = protowire.AppendFixed64(dst, uint64(in.val))
	}
	next := 0
	rest := stripped
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return nil, corrupt("member event is not well-formed protobuf")
		}
		vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if vn < 0 {
			return nil, corrupt("member event is not well-formed protobuf")
		}
		for next < np && pending[next].num < num {
			emit(pending[next])
			next++
		}
		if next < np && pending[next].num == num {
			return nil, corrupt("member event carries field %d that the envelope interns", num)
		}
		dst = append(dst, rest[:n+vn]...)
		rest = rest[n+vn:]
	}
	for ; next < np; next++ {
		emit(pending[next])
	}
	return dst, nil
}
