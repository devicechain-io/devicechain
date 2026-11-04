// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"unicode/utf8"

	"github.com/klauspost/compress/s2"
	"google.golang.org/protobuf/encoding/protowire"
)

// member is one decoded member record. event aliases the reader's raw bytes.
type member struct {
	index          uint32
	event          []byte
	refA, refB     uint32
	odelta, pdelta int64
	flags          uint32
}

// Reader is a parsed envelope. Parse does all the validation, so after it succeeds every
// accessor is a bounds-safe slice or a copy. A Reader holds the message bytes it was parsed
// from when the members were not compressed: the caller must not modify them while it is in
// use.
type Reader struct {
	hdr     Header
	base    int64
	dict    []string
	members []member
}

// Parse validates data as an envelope and returns a Reader over it.
//
// The version is read FIRST, before the checksum and before any field is walked: the version
// field is the first field of every version of the format, so a message of a version this
// reader does not know is ErrVersion however its other fields (or its checksum rule) have
// changed. A caller can then tell "written by a newer release" from "damaged".
//
// It then refuses, wrapping ErrCorrupt, what Encode could not have written: a checksum that
// does not match, a field out of order, repeated or outside the format, a declared count or raw
// size that disagrees with the members, a dictionary reference outside the dictionary, member
// indices that are not ascending, a member whose event bytes still carry a field the envelope
// interns (so Member and the typed decoder cannot disagree about it), more than MaxMembers
// settled indices. It does NOT promise to refuse every message Encode would not write (a
// member's event bytes are opaque beyond that), only that anything it accepts decodes the same
// through Member and through Parts. A member count or raw size beyond the bounds is
// ErrTooManyMembers or ErrTooLarge, checked before anything is inflated.
func Parse(data []byte) (*Reader, error) {
	// Field 1 (version), tag 0x08, as a varint.
	if len(data) < 2 || data[0] != byte(protowire.EncodeTag(fVersion, protowire.VarintType)) {
		return nil, fmt.Errorf("%w: the message does not begin with a version", ErrVersion)
	}
	if v, n := protowire.ConsumeVarint(data[1:]); n < 0 || v != Version {
		return nil, fmt.Errorf("%w: %d", ErrVersion, v)
	}
	if len(data) > maxMessageBytes {
		return nil, ErrTooLarge
	}
	if len(data) < checksumTailSize+1 {
		return nil, corrupt("%d bytes is too short", len(data))
	}
	body, tail := data[:len(data)-checksumTailSize], data[len(data)-checksumTailSize:]
	tag, typ, tn := protowire.ConsumeTag(tail)
	if tn != 1 || tag != fChecksum || typ != protowire.Fixed32Type {
		return nil, corrupt("no checksum")
	}
	if want := binary.LittleEndian.Uint32(tail[1:]); crc32.Checksum(body, crcTable) != want {
		return nil, corrupt("checksum mismatch")
	}

	r := &Reader{}
	var (
		version, kind, codec, count, rawSize uint64
		members                              []byte
		seen                                 uint32 // bit per singular field number
		last                                 protowire.Number
	)
	rest := body
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return nil, corrupt("bad field tag")
		}
		rest = rest[n:]
		if num < last {
			return nil, corrupt("field %d out of order", num)
		}
		if num != fDict && num == last {
			return nil, corrupt("field %d repeated", num)
		}
		last = num
		var err error
		switch num {
		case fVersion, fKind, fSourceSeq, fRound, fCodec, fCount, fRawSize:
			var v uint64
			v, n, err = consumeVarint(typ, rest)
			if err != nil {
				return nil, err
			}
			switch num {
			case fVersion:
				version = v
			case fKind:
				kind = v
			case fSourceSeq:
				r.hdr.SourceSeq = v
			case fRound:
				if v > 1<<32-1 {
					return nil, corrupt("round %d", v)
				}
				r.hdr.Round = uint32(v)
			case fCodec:
				codec = v
			case fCount:
				count = v
			case fRawSize:
				rawSize = v
			}
		case fTenant:
			var b []byte
			b, n, err = consumeBytes(typ, rest)
			if err != nil {
				return nil, err
			}
			if !utf8.Valid(b) {
				return nil, corrupt("tenant is not valid UTF-8")
			}
			r.hdr.Tenant = string(b)
		case fBaseTime:
			if typ != protowire.Fixed64Type {
				return nil, corrupt("base time has wire type %d", typ)
			}
			v, m := protowire.ConsumeFixed64(rest)
			if m < 0 {
				return nil, corrupt("base time is truncated")
			}
			r.base, n = int64(v), m
		case fDict:
			var b []byte
			b, n, err = consumeBytes(typ, rest)
			if err != nil {
				return nil, err
			}
			if len(r.dict) >= maxDictEntries || len(b) > maxDictEntryBytes || !utf8.Valid(b) {
				return nil, corrupt("dictionary entry")
			}
			r.dict = append(r.dict, string(b))
		case fMembers:
			members, n, err = consumeBytes(typ, rest)
			if err != nil {
				return nil, err
			}
		case fSettledElsewhere:
			var b []byte
			b, n, err = consumeBytes(typ, rest)
			if err != nil {
				return nil, err
			}
			for len(b) > 0 {
				v, m := protowire.ConsumeVarint(b)
				if m < 0 || v >= maxIndex {
					return nil, corrupt("settled index")
				}
				if l := len(r.hdr.SettledElsewhere); l > 0 && uint32(v) <= r.hdr.SettledElsewhere[l-1] {
					return nil, corrupt("settled indices are not ascending")
				}
				if len(r.hdr.SettledElsewhere) >= MaxMembers {
					return nil, ErrTooManyMembers
				}
				r.hdr.SettledElsewhere = append(r.hdr.SettledElsewhere, uint32(v))
				b = b[m:]
			}
		default:
			return nil, corrupt("unknown field %d", num)
		}
		rest = rest[n:]
		if num < 32 && num != fDict {
			seen |= 1 << num
		}
	}

	if version != Version {
		return nil, fmt.Errorf("%w: %d", ErrVersion, version) // unreachable: read first; kept as the invariant
	}
	r.hdr.Kind = Kind(kind)
	if kind > 0xff || !r.hdr.Kind.valid() {
		return nil, corrupt("kind %d", kind)
	}
	if r.hdr.Tenant == "" {
		return nil, corrupt("no tenant")
	}
	if count > MaxMembers {
		return nil, ErrTooManyMembers
	}
	if rawSize > MaxRawBytes {
		return nil, ErrTooLarge
	}
	if count == 0 {
		if members != nil || rawSize != 0 || codec != codecNone || seen&(1<<fMembers) != 0 {
			return nil, corrupt("members without a count")
		}
		if len(r.hdr.SettledElsewhere) == 0 {
			return nil, corrupt("no members and no settled indices")
		}
		return r, nil
	}

	var raw []byte
	switch codec {
	case codecNone:
		if uint64(len(members)) != rawSize {
			return nil, corrupt("raw size %d for %d bytes of members", rawSize, len(members))
		}
		raw = members
	case codecS2:
		// The declared length is read from the block header and compared before the block is
		// inflated, so a few bytes of input cannot ask for a large allocation.
		dl, err := s2.DecodedLen(members)
		if err != nil || uint64(dl) != rawSize {
			return nil, corrupt("compressed block does not hold %d bytes", rawSize)
		}
		raw, err = s2.Decode(make([]byte, dl), members)
		if err != nil {
			return nil, corrupt("compressed block: %v", err)
		}
	default:
		return nil, corrupt("codec %d", codec)
	}

	r.members = make([]member, 0, count)
	for len(raw) > 0 {
		rec, n := protowire.ConsumeBytes(raw)
		if n < 0 {
			return nil, corrupt("member framing")
		}
		raw = raw[n:]
		if uint64(len(r.members)) >= count {
			return nil, corrupt("more members than the declared %d", count)
		}
		m, err := parseMember(rec, len(r.dict), r.hdr.Kind)
		if err != nil {
			return nil, err
		}
		if l := len(r.members); l > 0 && m.index <= r.members[l-1].index {
			return nil, corrupt("member indices are not ascending")
		}
		r.members = append(r.members, m)
	}
	if uint64(len(r.members)) != count {
		return nil, corrupt("%d members for a declared %d", len(r.members), count)
	}
	for _, s := range r.hdr.SettledElsewhere {
		for i := range r.members {
			if r.members[i].index == s {
				return nil, corrupt("member %d is both carried and settled elsewhere", s)
			}
		}
	}
	return r, nil
}

func consumeVarint(typ protowire.Type, b []byte) (uint64, int, error) {
	if typ != protowire.VarintType {
		return 0, 0, corrupt("wire type %d for a varint field", typ)
	}
	v, n := protowire.ConsumeVarint(b)
	if n < 0 {
		return 0, 0, corrupt("bad varint")
	}
	return v, n, nil
}

func consumeBytes(typ protowire.Type, b []byte) ([]byte, int, error) {
	if typ != protowire.BytesType {
		return nil, 0, corrupt("wire type %d for a bytes field", typ)
	}
	v, n := protowire.ConsumeBytes(b)
	if n < 0 {
		return nil, 0, corrupt("bad length-delimited field")
	}
	return v, n, nil
}

func parseMember(rec []byte, dictLen int, kind Kind) (member, error) {
	var m member
	var last protowire.Number
	for len(rec) > 0 {
		num, typ, n := protowire.ConsumeTag(rec)
		if n < 0 {
			return m, corrupt("member field tag")
		}
		rec = rec[n:]
		if num <= last {
			return m, corrupt("member field %d out of order or repeated", num)
		}
		last = num
		switch num {
		case mIndex, mOccurredDelta, mProcessedDelta, mDeviceTypeRef, mProfileRef, mFlags:
			v, vn, err := consumeVarint(typ, rec)
			if err != nil {
				return m, err
			}
			rec = rec[vn:]
			switch num {
			case mIndex:
				if v >= maxIndex {
					return m, corrupt("member index %d", v)
				}
				m.index = uint32(v)
			case mOccurredDelta:
				m.odelta = protowire.DecodeZigZag(v)
			case mProcessedDelta:
				m.pdelta = protowire.DecodeZigZag(v)
			case mDeviceTypeRef, mProfileRef:
				if kind != KindResolved || v == 0 || v > uint64(dictLen) {
					return m, corrupt("dictionary reference %d of %d", v, dictLen)
				}
				if num == mDeviceTypeRef {
					m.refA = uint32(v)
				} else {
					m.refB = uint32(v)
				}
			case mFlags:
				if v == 0 || v&^flagMask != 0 {
					return m, corrupt("member flags %d", v)
				}
				m.flags = uint32(v)
			}
		case mEvent:
			b, vn, err := consumeBytes(typ, rec)
			if err != nil {
				return m, err
			}
			rec = rec[vn:]
			if len(b) == 0 {
				return m, corrupt("empty event")
			}
			if err := checkStripped(b, layoutFor(kind)); err != nil {
				return m, err
			}
			m.event = b
		default:
			return m, corrupt("unknown member field %d", num)
		}
	}
	if m.flags&flagNoOccurred != 0 && m.odelta != 0 || m.flags&flagNoProcessed != 0 && m.pdelta != 0 {
		return m, corrupt("a delta for an instant the member does not carry")
	}
	return m, nil
}

// checkStripped refuses member event bytes that are not well-formed protobuf or that still carry
// a field the envelope interns. Such a member was not produced by Encode, and it would have two
// sources of truth for the field (the bytes and the member's refs or deltas) that the two
// decoders resolve differently.
func checkStripped(event []byte, lay *layout) error {
	for rest := event; len(rest) > 0; {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return corrupt("member event is not well-formed protobuf")
		}
		vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if vn < 0 {
			return corrupt("member event is not well-formed protobuf")
		}
		if (lay.refA != 0 && num == lay.refA) || (lay.refB != 0 && num == lay.refB) ||
			num == lay.occurred || num == lay.processed {
			return corrupt("member event carries field %d that the envelope interns", num)
		}
		rest = rest[n+vn:]
	}
	return nil
}

// Header returns what the envelope says about its members. The slice of settled indices is
// the reader's own: do not modify it.
func (r *Reader) Header() Header { return r.hdr }

// Len is the number of members the envelope carries.
func (r *Reader) Len() int { return len(r.members) }

// Index is the source position of the i'th member.
func (r *Reader) Index(i int) uint32 { return r.members[i].index }

// VerifyTenant checks the envelope's tenant against the one the subject names. A mismatch is
// poison: the envelope is for another tenant than the subject it was delivered on.
func (r *Reader) VerifyTenant(tenant string) error {
	if r.hdr.Tenant != tenant {
		return fmt.Errorf("%w: envelope says %q, subject says %q", ErrTenant, r.hdr.Tenant, tenant)
	}
	return nil
}

// Parts is a member split into the event bytes without the interned fields and the values of
// the ones taken out. A decoder that parses the event itself fills its fields from these
// instead of from bytes, and shares the dictionary's strings across the whole envelope.
type Parts struct {
	// Event is the member's event bytes with the interned fields omitted. It aliases the
	// reader: read it, do not retain it past the reader or modify it.
	Event []byte
	// DeviceTypeToken and ProfileVersionToken are the resolved event's fields 11 and 12;
	// the Has flags say whether the event carried them (an empty string can be carried).
	DeviceTypeToken, ProfileVersionToken       string
	HasDeviceTypeToken, HasProfileVersionToken bool
	// Occurred and Processed are the instants as nanoseconds since the epoch, when the event
	// carried the numeric form; otherwise the event bytes carry the string form themselves.
	Occurred, Processed       int64
	HasOccurred, HasProcessed bool
}

// Parts returns the i'th member's pieces.
func (r *Reader) Parts(i int) Parts {
	m := &r.members[i]
	p := Parts{
		Event:       m.event,
		HasOccurred: m.flags&flagNoOccurred == 0, HasProcessed: m.flags&flagNoProcessed == 0,
	}
	if p.HasOccurred {
		p.Occurred = r.base + m.odelta
	}
	if p.HasProcessed {
		p.Processed = r.base + m.pdelta
	}
	if m.refA != 0 {
		p.DeviceTypeToken, p.HasDeviceTypeToken = r.dict[m.refA-1], true
	}
	if m.refB != 0 {
		p.ProfileVersionToken, p.HasProfileVersionToken = r.dict[m.refB-1], true
	}
	return p
}

// AppendMember appends to dst the i'th member as a complete, self-contained legacy event:
// the bytes the producer's marshaller wrote, which any legacy decoder reads. A failed-event
// record carries these bytes.
func (r *Reader) AppendMember(dst []byte, i int) ([]byte, error) {
	p := r.Parts(i)
	return rebuild(dst, p.Event, layoutFor(r.hdr.Kind),
		p.DeviceTypeToken, p.ProfileVersionToken, p.HasDeviceTypeToken, p.HasProfileVersionToken,
		p.Occurred, p.Processed, p.HasOccurred, p.HasProcessed)
}

// Member is AppendMember into a new slice.
func (r *Reader) Member(i int) ([]byte, error) {
	return r.AppendMember(make([]byte, 0, len(r.members[i].event)+48), i)
}
