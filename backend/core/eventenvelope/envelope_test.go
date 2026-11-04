// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"net/textproto"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/klauspost/compress/s2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// The size gate: sixteen realistic resolved events in one message. Today they are sixteen
// messages of 138 B each and ~490 B delivered; the design's budget is 700 B.
func TestSixteenRealisticEventsFitSevenHundredBytes(t *testing.T) {
	for name, gen := range map[string]func(int) *resolvedevent.ResolvedEvent{
		"loadtest": loadtestResolved, "heavier": realisticResolved,
	} {
		events := corpusOf(t, 16, gen)
		raw := 0
		for _, e := range events {
			raw += len(e.Bytes)
		}
		msg, err := eventenvelope.Encode(resolvedHeader(), events)
		require.NoError(t, err)
		t.Logf("%s: 16 events, %d B of legacy payloads (%d B each) -> %d B envelope (%.1f B/event)",
			name, raw, raw/16, len(msg), float64(len(msg))/16)
		if name == "loadtest" {
			assert.LessOrEqual(t, len(msg), 700)
		}
	}
}

// Every member comes back from the envelope as the exact bytes that went in, and decodes to
// the same event as the legacy decoder gives for those bytes.
func TestRoundTripIsByteIdenticalAndDecodesLikeLegacy(t *testing.T) {
	for _, k := range []int{2, 3, 4, 16, 64} {
		for _, format := range []eventtime.WireFormat{eventtime.WireCompact, eventtime.WireLegacy} {
			events := make([]eventenvelope.Event, k)
			for i := range events {
				events[i] = eventenvelope.Event{Index: uint32(i), Bytes: legacyBytes(t, realisticResolved(i), format)}
			}
			msg, err := eventenvelope.Encode(resolvedHeader(), events)
			require.NoError(t, err, "k=%d", k)
			r, err := eventenvelope.Parse(msg)
			require.NoError(t, err, "k=%d", k)
			require.Equal(t, k, r.Len())
			require.NoError(t, r.VerifyTenant("acme"))
			assert.Equal(t, uint64(4242), r.Header().SourceSeq)
			for i := range events {
				got, err := r.Member(i)
				require.NoError(t, err)
				assert.Equal(t, events[i].Bytes, got, "k=%d member %d format %v", k, i, format)
				want, err := resolvedevent.UnmarshalResolvedEvent(events[i].Bytes)
				require.NoError(t, err)
				typed, err := resolvedevent.UnmarshalEnvelopeMember(r, i)
				require.NoError(t, err)
				assert.Equal(t, want, typed, "k=%d member %d format %v", k, i, format)
			}
		}
	}
}

// Instants the numeric form cannot hold travel as strings inside the event bytes, and a
// member without a numeric instant must not be read as "equal to the base time".
func TestMembersWithoutNumericInstantsRoundTrip(t *testing.T) {
	old := realisticResolved(1)
	old.OccurredTime = time.Date(1500, 1, 2, 3, 4, 5, 6, time.UTC) // before 1677
	old.ProcessedTime = time.Date(2300, 1, 2, 3, 4, 5, 6, time.UTC)
	mixed := realisticResolved(2)
	mixed.ProcessedTime = time.Date(2300, 1, 2, 3, 4, 5, 6, time.UTC)
	for _, set := range [][]*resolvedevent.ResolvedEvent{
		{old, realisticResolved(0)},
		{realisticResolved(0), old, mixed},
		{old, mixed},
	} {
		var events []eventenvelope.Event
		for i, e := range set {
			events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: legacyBytes(t, e, eventtime.WireCompact)})
		}
		msg, err := eventenvelope.Encode(resolvedHeader(), events)
		require.NoError(t, err)
		r, err := eventenvelope.Parse(msg)
		require.NoError(t, err)
		for i := range events {
			got, err := r.Member(i)
			require.NoError(t, err)
			assert.Equal(t, events[i].Bytes, got)
			want, err := resolvedevent.UnmarshalResolvedEvent(events[i].Bytes)
			require.NoError(t, err)
			typed, err := resolvedevent.UnmarshalEnvelopeMember(r, i)
			require.NoError(t, err)
			assert.Equal(t, want, typed)
		}
	}
}

// An optional string that is present and empty is not absent.
func TestEmptyButPresentTokensAreKept(t *testing.T) {
	e := realisticResolved(1)
	e.DeviceTypeToken = ""
	e.ProfileVersionToken = "" // the marshaller omits an empty one: absent
	a := legacyBytes(t, e, eventtime.WireCompact)
	var withEmpty []byte // field 11 present with an empty value
	{
		var b []byte
		rest := a
		for len(rest) > 0 {
			num, typ, n := protowire.ConsumeTag(rest)
			vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
			if num == 13 || num == 17 { // before the first field that follows 11/12
				b = protowire.AppendTag(b, 11, protowire.BytesType)
				b = protowire.AppendString(b, "")
				b = append(b, rest...)
				break
			}
			b = append(b, rest[:n+vn]...)
			rest = rest[n+vn:]
		}
		withEmpty = b
	}
	events := []eventenvelope.Event{{Index: 0, Bytes: withEmpty}, {Index: 1, Bytes: legacyBytes(t, realisticResolved(2), eventtime.WireCompact)}}
	msg, err := eventenvelope.Encode(resolvedHeader(), events)
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	p := r.Parts(0)
	assert.True(t, p.HasDeviceTypeToken)
	assert.Empty(t, p.DeviceTypeToken)
	assert.False(t, p.HasProfileVersionToken)
	got, err := r.Member(0)
	require.NoError(t, err)
	assert.Equal(t, withEmpty, got)
}

func TestUnresolvedKindRoundTrip(t *testing.T) {
	at := t0.UnixNano()
	var events []eventenvelope.Event
	for i := 0; i < 20; i++ {
		occ, proc := at+int64(i)*1000, at+int64(i)*1000+500
		var op, pp *int64
		if i%5 != 3 { // some events carry the string forms only
			op = &occ
		}
		if i%7 != 2 {
			pp = &proc
		}
		events = append(events, eventenvelope.Event{
			Index: uint32(i * 2), // a later round: indices are the source's
			Bytes: unresolvedBytes("mqtt", "dev-"+string(rune('a'+i)), 1, []byte{1, 2, 3, byte(i)}, op, pp),
		})
	}
	h := eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme", Round: 1, SettledElsewhere: []uint32{1, 3, 5}}
	msg, err := eventenvelope.Encode(h, events)
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	assert.Equal(t, h, r.Header())
	for i := range events {
		assert.Equal(t, events[i].Index, r.Index(i))
		got, err := r.Member(i)
		require.NoError(t, err)
		assert.Equal(t, events[i].Bytes, got)
	}
}

func TestSettlementRecordWithNoMembers(t *testing.T) {
	h := eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme", SourceSeq: 9, SettledElsewhere: []uint32{0, 1, 2}}
	msg, err := eventenvelope.Encode(h, nil)
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	assert.Equal(t, 0, r.Len())
	assert.Equal(t, h, r.Header())
}

func TestMarkedReadsTheHeader(t *testing.T) {
	assert.False(t, eventenvelope.Marked(nil))
	assert.False(t, eventenvelope.Marked(textproto.MIMEHeader{}))
	assert.True(t, eventenvelope.Marked(textproto.MIMEHeader{eventenvelope.HeaderName: {eventenvelope.HeaderValue}}))
}

// ---- bounds ----------------------------------------------------------------------------

func TestEncodeRefusesWhatTheBoundsForbid(t *testing.T) {
	t.Run("k = 1 is the legacy shape", func(t *testing.T) {
		_, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 1))
		assert.ErrorIs(t, err, eventenvelope.ErrSingleMember)
	})
	t.Run("k = 1 is allowed as a later round", func(t *testing.T) {
		h := resolvedHeader()
		h.Round = 1
		_, err := eventenvelope.Encode(h, corpus(t, 1))
		assert.NoError(t, err)
	})
	t.Run("k = 64 fits, 65 is refused", func(t *testing.T) {
		_, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 64))
		require.NoError(t, err)
		_, err = eventenvelope.Encode(resolvedHeader(), corpus(t, 65))
		assert.ErrorIs(t, err, eventenvelope.ErrTooManyMembers)
	})
	t.Run("raw size beyond 64 KiB is refused, exactly at it is not", func(t *testing.T) {
		mk := func(payload int) []eventenvelope.Event {
			big := unresolvedBytes("s", "d", 1, make([]byte, payload), nil, nil)
			return []eventenvelope.Event{{Index: 0, Bytes: big}, {Index: 1, Bytes: unresolvedBytes("s", "d", 1, []byte{1}, nil, nil)}}
		}
		h := eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme"}
		// Find the largest payload that still fits, by the refusal itself.
		lo, hi := 1000, eventenvelope.MaxRawBytes
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if _, err := eventenvelope.Encode(h, mk(mid)); err == nil {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		msg, err := eventenvelope.Encode(h, mk(lo))
		require.NoError(t, err)
		_, err = eventenvelope.Encode(h, mk(lo+1))
		assert.ErrorIs(t, err, eventenvelope.ErrTooLarge)
		// The largest accepted envelope holds EXACTLY 64 KiB of members, not one byte less.
		assert.Equal(t, eventenvelope.MaxRawBytes, declaredRawSize(t, msg))
	})
	t.Run("bad requests", func(t *testing.T) {
		ev := corpus(t, 3)
		for name, h := range map[string]eventenvelope.Header{
			"no kind":                   {Tenant: "acme"},
			"no tenant":                 {Kind: eventenvelope.KindResolved},
			"settled overlaps a member": {Kind: eventenvelope.KindResolved, Tenant: "acme", SettledElsewhere: []uint32{1}},
			"settled not ascending":     {Kind: eventenvelope.KindResolved, Tenant: "acme", SettledElsewhere: []uint32{9, 8}},
		} {
			_, err := eventenvelope.Encode(h, ev)
			assert.ErrorIs(t, err, eventenvelope.ErrBadInput, name)
		}
		_, err := eventenvelope.Encode(resolvedHeader(), []eventenvelope.Event{ev[1], ev[0]})
		assert.ErrorIs(t, err, eventenvelope.ErrBadInput, "indices out of order")
		_, err = eventenvelope.Encode(resolvedHeader(), []eventenvelope.Event{ev[0], {Index: 1, Bytes: []byte{0xff, 0xff}}})
		assert.ErrorIs(t, err, eventenvelope.ErrBadInput, "an event that is not protobuf")
		_, err = eventenvelope.Encode(resolvedHeader(), nil)
		assert.ErrorIs(t, err, eventenvelope.ErrEmpty)
	})
}

// head builds the start of a message by hand, up to (not including) the members.
func head(version, kind uint64, tenant string, extra ...func([]byte) []byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, version)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, kind)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, tenant)
	for _, f := range extra {
		b = f(b)
	}
	return b
}

func varint(num protowire.Number, v uint64) func([]byte) []byte {
	return func(b []byte) []byte {
		b = protowire.AppendTag(b, num, protowire.VarintType)
		return protowire.AppendVarint(b, v)
	}
}

func bytesField(num protowire.Number, v []byte) func([]byte) []byte {
	return func(b []byte) []byte {
		b = protowire.AppendTag(b, num, protowire.BytesType)
		return protowire.AppendBytes(b, v)
	}
}

// A reader refuses what the bounds forbid before it allocates for it, so a hostile or damaged
// message cannot ask for more than 64 KiB.
func TestParseRefusesWhatTheBoundsForbid(t *testing.T) {
	next := uint64(0)
	member := func(n int) []byte { // one member record of n bytes of event, the next index
		var rec []byte
		if next++; next > 1 {
			rec = protowire.AppendTag(rec, 1, protowire.VarintType)
			rec = protowire.AppendVarint(rec, next-1)
		}
		rec = protowire.AppendTag(rec, 6, protowire.BytesType)
		rec = protowire.AppendBytes(rec, make([]byte, n))
		return protowire.AppendBytes(nil, rec)
	}
	t.Run("count 65", func(t *testing.T) {
		raw := member(3)
		msg := seal(head(1, 2, "acme", varint(9, 65), bytesField(10, raw), varint(12, uint64(len(raw)))))
		_, err := eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrTooManyMembers)
	})
	t.Run("raw size 64 KiB + 1", func(t *testing.T) {
		raw := member(3)
		msg := seal(head(1, 2, "acme", varint(9, 2), bytesField(10, raw), varint(12, eventenvelope.MaxRawBytes+1)))
		_, err := eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrTooLarge)
	})
	t.Run("a small block that declares a large expansion", func(t *testing.T) {
		// An s2 block whose decoded length is 1 GiB, in a handful of bytes.
		bomb := protowire.AppendVarint(nil, 1<<30)
		bomb = append(bomb, 0x00, 0x00)
		msg := seal(head(1, 2, "acme", varint(8, 1), varint(9, 2), bytesField(10, bomb), varint(12, 1<<30)))
		_, err := eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrTooLarge)
		// The declared raw size is the bound's: a block that expands beyond it is corrupt.
		msg = seal(head(1, 2, "acme", varint(8, 1), varint(9, 2), bytesField(10, bomb), varint(12, 100)))
		_, err = eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
	})
	t.Run("a block that decodes to other than its declared size", func(t *testing.T) {
		raw := append(member(40), member(40)...)
		block := s2.Encode(nil, raw)
		msg := seal(head(1, 2, "acme", varint(8, 1), varint(9, 2), bytesField(10, block), varint(12, uint64(len(raw)+1))))
		_, err := eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
	})
	t.Run("the control: the same hand-built message with honest numbers parses", func(t *testing.T) {
		raw := append(member(40), member(40)...)
		msg := seal(head(1, 2, "acme", varint(9, 2), bytesField(10, raw), varint(12, uint64(len(raw)))))
		r, err := eventenvelope.Parse(msg)
		require.NoError(t, err)
		assert.Equal(t, 2, r.Len())
		block := s2.Encode(nil, raw)
		msg = seal(head(1, 2, "acme", varint(8, 1), varint(9, 2), bytesField(10, block), varint(12, uint64(len(raw)))))
		r, err = eventenvelope.Parse(msg)
		require.NoError(t, err)
		assert.Equal(t, 2, r.Len())
	})
	t.Run("version and kind", func(t *testing.T) {
		raw := append(member(40), member(40)...)
		body := func(v, k uint64) []byte {
			return seal(head(v, k, "acme", varint(9, 2), bytesField(10, raw), varint(12, uint64(len(raw)))))
		}
		_, err := eventenvelope.Parse(body(2, 2))
		assert.ErrorIs(t, err, eventenvelope.ErrVersion)
		_, err = eventenvelope.Parse(body(1, 0))
		assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
		_, err = eventenvelope.Parse(body(1, 3))
		assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
	})
	t.Run("a message over the bound", func(t *testing.T) {
		_, err := eventenvelope.Parse(make([]byte, 1<<20))
		assert.ErrorIs(t, err, eventenvelope.ErrTooLarge)
	})
}

func TestTenantMustMatchTheSubject(t *testing.T) {
	msg, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 4))
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	assert.NoError(t, r.VerifyTenant("acme"))
	assert.ErrorIs(t, r.VerifyTenant("globex"), eventenvelope.ErrTenant)
}

func TestSmallEnvelopesAreNotCompressedAndLargeOnesAre(t *testing.T) {
	small, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 2))
	require.NoError(t, err)
	large, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 32))
	require.NoError(t, err)
	has := func(msg []byte) bool { // field 8 present
		rest := msg[:len(msg)-5]
		for len(rest) > 0 {
			num, typ, n := protowire.ConsumeTag(rest)
			if num == 8 {
				return true
			}
			rest = rest[n+protowire.ConsumeFieldValue(num, typ, rest[n:]):]
		}
		return false
	}
	assert.False(t, has(small))
	assert.True(t, has(large))
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 16))
	require.NoError(t, err)
	b, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 16))
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

// declaredRawSize reads the raw_size field (12) of an envelope message.
func declaredRawSize(t *testing.T, msg []byte) int {
	t.Helper()
	rest := msg[:len(msg)-5]
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		require.GreaterOrEqual(t, n, 0)
		if num == 12 {
			v, m := protowire.ConsumeVarint(rest[n:])
			require.GreaterOrEqual(t, m, 0)
			return int(v)
		}
		rest = rest[n+protowire.ConsumeFieldValue(num, typ, rest[n:]):]
	}
	t.Fatal("no raw_size field")
	return 0
}
