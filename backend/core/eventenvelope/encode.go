// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope

import (
	"hash/crc32"
	"unicode/utf8"

	"github.com/klauspost/compress/s2"
	"google.golang.org/protobuf/encoding/protowire"
)

// PEventEnvelope field numbers, from dc-event-envelope.proto.
const (
	fVersion          = protowire.Number(1)
	fKind             = protowire.Number(2)
	fTenant           = protowire.Number(3)
	fSourceSeq        = protowire.Number(4)
	fRound            = protowire.Number(5)
	fBaseTime         = protowire.Number(6)
	fDict             = protowire.Number(7)
	fCodec            = protowire.Number(8)
	fCount            = protowire.Number(9)
	fMembers          = protowire.Number(10)
	fSettledElsewhere = protowire.Number(11)
	fRawSize          = protowire.Number(12)
	fChecksum         = protowire.Number(13)
)

// PEnvelopeMember field numbers.
const (
	mIndex          = protowire.Number(1)
	mOccurredDelta  = protowire.Number(2)
	mProcessedDelta = protowire.Number(3)
	mDeviceTypeRef  = protowire.Number(4)
	mProfileRef     = protowire.Number(5)
	mEvent          = protowire.Number(6)
	mFlags          = protowire.Number(7)
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// checksumTailSize is the checksum field: a one-byte tag and four bytes.
const checksumTailSize = 1 + 4

// Encode writes the envelope for events under h and returns the message payload. The caller
// publishes it with the HeaderName header set to HeaderValue.
//
// Each event is the full bytes of a PUnresolvedEvent or PResolvedEvent as its marshaller
// wrote them. An envelope of exactly one event is refused (ErrSingleMember) unless it is a
// later round or settles members elsewhere: one event is published in the legacy shape. More
// than MaxMembers events, or more than MaxRawBytes of member bytes, is refused too
// (ErrTooManyMembers, ErrTooLarge): splitting is the producer's decision, never this
// function's.
func Encode(h Header, events []Event) ([]byte, error) {
	if !h.Kind.valid() {
		return nil, badInput("kind %d", uint8(h.Kind))
	}
	if h.Tenant == "" {
		return nil, badInput("empty tenant")
	}
	if !utf8.ValidString(h.Tenant) {
		return nil, badInput("tenant is not valid UTF-8")
	}
	k := len(events)
	if k > MaxMembers {
		return nil, ErrTooManyMembers
	}
	if k == 0 && len(h.SettledElsewhere) == 0 {
		return nil, ErrEmpty
	}
	if k == 1 && h.Round == 0 && len(h.SettledElsewhere) == 0 {
		return nil, ErrSingleMember
	}
	if err := checkAscending(h.SettledElsewhere); err != nil {
		return nil, err
	}
	for i, e := range events {
		if i > 0 && e.Index <= events[i-1].Index {
			return nil, badInput("member indices are not strictly ascending")
		}
		if e.Index >= maxIndex {
			return nil, badInput("member index %d", e.Index)
		}
	}
	if err := disjoint(h.SettledElsewhere, events); err != nil {
		return nil, err
	}

	lay := layoutFor(h.Kind)

	// Take the interned fields out of every event into one arena, interning the strings.
	total := 0
	for _, e := range events {
		total += len(e.Bytes)
	}
	arena := make([]byte, 0, total)
	ms := make([]parts, k)
	var dict []string
	intern := func(b []byte) (uint32, error) {
		if !utf8.Valid(b) {
			return 0, badInput("event carries a token that is not valid UTF-8")
		}
		if len(b) > maxDictEntryBytes {
			return 0, badInput("event carries a %d-byte token", len(b))
		}
		for i, s := range dict {
			if s == string(b) {
				return uint32(i + 1), nil
			}
		}
		if len(dict) >= maxDictEntries {
			return 0, badInput("dictionary is full")
		}
		dict = append(dict, string(b))
		return uint32(len(dict)), nil
	}
	for i, e := range events {
		var a, b []byte
		var hasA, hasB bool
		start := len(arena)
		var err error
		arena, ms[i], a, b, hasA, hasB, err = strip(arena, e.Bytes, lay)
		if err != nil {
			return nil, err
		}
		ms[i].event = arena[start:len(arena):len(arena)]
		if hasA {
			if ms[i].refA, err = intern(a); err != nil {
				return nil, err
			}
		}
		if hasB {
			if ms[i].refB, err = intern(b); err != nil {
				return nil, err
			}
		}
	}

	// The base time is the first numeric occurred instant, so the first member's delta is
	// usually zero and a burst's deltas are small.
	var base int64
	for i := range ms {
		if ms[i].hasOcc {
			base = ms[i].occurred
			break
		}
	}

	// The member records.
	rawSize := 0
	for i := range ms {
		rawSize += memberRecordSize(events[i].Index, &ms[i], base)
		if rawSize > MaxRawBytes {
			return nil, ErrTooLarge
		}
	}
	raw := make([]byte, 0, rawSize)
	for i := range ms {
		raw = appendMemberRecord(raw, events[i].Index, &ms[i], base)
	}

	body, codec := raw, uint64(codecNone)
	if len(raw) > CompressMin {
		if c := s2.Encode(nil, raw); len(c) < len(raw) {
			body, codec = c, codecS2
		}
	}

	// The message.
	size := protowire.SizeTag(fVersion) + protowire.SizeVarint(Version) +
		protowire.SizeTag(fKind) + protowire.SizeVarint(uint64(h.Kind)) +
		protowire.SizeTag(fTenant) + protowire.SizeBytes(len(h.Tenant)) +
		checksumTailSize + 64
	for _, s := range dict {
		size += protowire.SizeTag(fDict) + protowire.SizeBytes(len(s))
	}
	size += len(body) + 4*len(h.SettledElsewhere)
	out := make([]byte, 0, size)
	out = protowire.AppendTag(out, fVersion, protowire.VarintType)
	out = protowire.AppendVarint(out, Version)
	out = protowire.AppendTag(out, fKind, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(h.Kind))
	out = protowire.AppendTag(out, fTenant, protowire.BytesType)
	out = protowire.AppendString(out, h.Tenant)
	if h.SourceSeq != 0 {
		out = protowire.AppendTag(out, fSourceSeq, protowire.VarintType)
		out = protowire.AppendVarint(out, h.SourceSeq)
	}
	if h.Round != 0 {
		out = protowire.AppendTag(out, fRound, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(h.Round))
	}
	if base != 0 {
		out = protowire.AppendTag(out, fBaseTime, protowire.Fixed64Type)
		out = protowire.AppendFixed64(out, uint64(base))
	}
	for _, s := range dict {
		out = protowire.AppendTag(out, fDict, protowire.BytesType)
		out = protowire.AppendString(out, s)
	}
	if codec != codecNone {
		out = protowire.AppendTag(out, fCodec, protowire.VarintType)
		out = protowire.AppendVarint(out, codec)
	}
	if k != 0 {
		out = protowire.AppendTag(out, fCount, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(k))
		out = protowire.AppendTag(out, fMembers, protowire.BytesType)
		out = protowire.AppendBytes(out, body)
	}
	if len(h.SettledElsewhere) > 0 {
		packed := 0
		for _, v := range h.SettledElsewhere {
			packed += protowire.SizeVarint(uint64(v))
		}
		out = protowire.AppendTag(out, fSettledElsewhere, protowire.BytesType)
		out = protowire.AppendVarint(out, uint64(packed))
		for _, v := range h.SettledElsewhere {
			out = protowire.AppendVarint(out, uint64(v))
		}
	}
	if k != 0 {
		out = protowire.AppendTag(out, fRawSize, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(len(raw)))
	}
	sum := crc32.Checksum(out, crcTable)
	out = protowire.AppendTag(out, fChecksum, protowire.Fixed32Type)
	out = protowire.AppendFixed32(out, sum)
	return out, nil
}

func checkAscending(v []uint32) error {
	for i, x := range v {
		if x >= maxIndex || (i > 0 && x <= v[i-1]) {
			return badInput("settled indices are not strictly ascending")
		}
	}
	return nil
}

func disjoint(settled []uint32, events []Event) error {
	for _, s := range settled {
		for _, e := range events {
			if e.Index == s {
				return badInput("member %d is both carried and settled elsewhere", s)
			}
		}
	}
	return nil
}

// memberFlags and the deltas of one member against base.
func memberDeltas(p *parts, base int64) (flags uint64, od, pd int64) {
	if p.hasOcc {
		od = p.occurred - base
	} else {
		flags |= flagNoOccurred
	}
	if p.hasProc {
		pd = p.processed - base
	} else {
		flags |= flagNoProcessed
	}
	return flags, od, pd
}

func zigzag(v int64) uint64 { return protowire.EncodeZigZag(v) }

func memberRecordSize(index uint32, p *parts, base int64) int {
	flags, od, pd := memberDeltas(p, base)
	n := memberBodySize(index, p, flags, od, pd)
	return protowire.SizeBytes(n)
}

func memberBodySize(index uint32, p *parts, flags uint64, od, pd int64) int {
	n := 0
	if index != 0 {
		n += protowire.SizeTag(mIndex) + protowire.SizeVarint(uint64(index))
	}
	if od != 0 {
		n += protowire.SizeTag(mOccurredDelta) + protowire.SizeVarint(zigzag(od))
	}
	if pd != 0 {
		n += protowire.SizeTag(mProcessedDelta) + protowire.SizeVarint(zigzag(pd))
	}
	if p.refA != 0 {
		n += protowire.SizeTag(mDeviceTypeRef) + protowire.SizeVarint(uint64(p.refA))
	}
	if p.refB != 0 {
		n += protowire.SizeTag(mProfileRef) + protowire.SizeVarint(uint64(p.refB))
	}
	if len(p.event) > 0 {
		n += protowire.SizeTag(mEvent) + protowire.SizeBytes(len(p.event))
	}
	if flags != 0 {
		n += protowire.SizeTag(mFlags) + protowire.SizeVarint(flags)
	}
	return n
}

func appendMemberRecord(dst []byte, index uint32, p *parts, base int64) []byte {
	flags, od, pd := memberDeltas(p, base)
	dst = protowire.AppendVarint(dst, uint64(memberBodySize(index, p, flags, od, pd)))
	if index != 0 {
		dst = protowire.AppendTag(dst, mIndex, protowire.VarintType)
		dst = protowire.AppendVarint(dst, uint64(index))
	}
	if od != 0 {
		dst = protowire.AppendTag(dst, mOccurredDelta, protowire.VarintType)
		dst = protowire.AppendVarint(dst, zigzag(od))
	}
	if pd != 0 {
		dst = protowire.AppendTag(dst, mProcessedDelta, protowire.VarintType)
		dst = protowire.AppendVarint(dst, zigzag(pd))
	}
	if p.refA != 0 {
		dst = protowire.AppendTag(dst, mDeviceTypeRef, protowire.VarintType)
		dst = protowire.AppendVarint(dst, uint64(p.refA))
	}
	if p.refB != 0 {
		dst = protowire.AppendTag(dst, mProfileRef, protowire.VarintType)
		dst = protowire.AppendVarint(dst, uint64(p.refB))
	}
	if len(p.event) > 0 {
		dst = protowire.AppendTag(dst, mEvent, protowire.BytesType)
		dst = protowire.AppendBytes(dst, p.event)
	}
	if flags != 0 {
		dst = protowire.AppendTag(dst, mFlags, protowire.VarintType)
		dst = protowire.AppendVarint(dst, flags)
	}
	return dst
}
