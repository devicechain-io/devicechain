// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"fmt"
	"net/textproto"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// withEmptyDeviceType returns the bytes of a resolved event with field 11 (the device-type
// token) present and EMPTY, written where field order puts it.
func withEmptyDeviceType(t testing.TB, legacy []byte) []byte {
	t.Helper()
	var out []byte
	rest := legacy
	done := false
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if !done && num > 11 {
			out = protowire.AppendTag(out, 11, protowire.BytesType)
			out = protowire.AppendString(out, "")
			done = true
		}
		if num != 11 { // an existing token is replaced by the empty one
			out = append(out, rest[:n+vn]...)
		}
		rest = rest[n+vn:]
	}
	if !done { // nothing above field 11: it goes last
		out = protowire.AppendTag(out, 11, protowire.BytesType)
		out = protowire.AppendString(out, "")
	}
	return out
}

// The version is read before anything else, so a message of a newer format is "unknown version"
// - not "corrupt", which a reader treats as poison - however the rest of it has changed.
func TestAFutureVersionIsReportedAsSuchWhateverElseChanged(t *testing.T) {
	// Version 2, then fields this reader has never heard of, no checksum at all.
	var future []byte
	future = protowire.AppendTag(future, 1, protowire.VarintType)
	future = protowire.AppendVarint(future, 2)
	future = protowire.AppendTag(future, 40, protowire.BytesType)
	future = protowire.AppendString(future, "something new")
	_, err := eventenvelope.Parse(future)
	assert.ErrorIs(t, err, eventenvelope.ErrVersion)
	assert.NotErrorIs(t, err, eventenvelope.ErrCorrupt)
	_, err = eventenvelope.Parse(seal(future))
	assert.ErrorIs(t, err, eventenvelope.ErrVersion)

	// Version 0, and a message that does not begin with a version at all.
	_, err = eventenvelope.Parse(seal([]byte{0x08, 0x00, 0x10, 0x02}))
	assert.ErrorIs(t, err, eventenvelope.ErrVersion)
	_, err = eventenvelope.Parse([]byte{0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	assert.ErrorIs(t, err, eventenvelope.ErrVersion)

	// And the control: version 1 is read as before.
	msg, err := eventenvelope.Encode(resolvedHeader(), corpus(t, 3))
	require.NoError(t, err)
	_, err = eventenvelope.Parse(msg)
	assert.NoError(t, err)
}

func TestTheMarkerMustNameThisVersion(t *testing.T) {
	assert.True(t, eventenvelope.Marked(textproto.MIMEHeader{eventenvelope.HeaderName: {eventenvelope.HeaderValue}}))
	assert.False(t, eventenvelope.Marked(textproto.MIMEHeader{eventenvelope.HeaderName: {"2"}}), "another version is not this format")
	assert.False(t, eventenvelope.Marked(textproto.MIMEHeader{eventenvelope.HeaderName: {"garbage"}}))
}

// Encode never accepts an event whose Member(i) would differ from it. An event that Encode
// takes is returned byte for byte; one it cannot promise that for is refused, and the producer
// publishes it in the legacy shape.
func TestEncodeRefusesWhatItCouldNotGiveBackByteForByte(t *testing.T) {
	ok := legacyBytes(t, loadtestResolved(1), eventtime.WireCompact)
	other := legacyBytes(t, loadtestResolved(2), eventtime.WireCompact)
	pair := func(first []byte) []eventenvelope.Event {
		return []eventenvelope.Event{{Index: 0, Bytes: first}, {Index: 1, Bytes: other}}
	}
	appendField := func(b []byte, num protowire.Number, v string) []byte {
		b = append([]byte(nil), b...)
		b = protowire.AppendTag(b, num, protowire.BytesType)
		return protowire.AppendString(b, v)
	}

	t.Run("a field after 18 in ascending order is kept exactly, as an unknown field is", func(t *testing.T) {
		// proto.Marshal writes unknown fields last; one with a number above 18 is in order.
		ev := appendField(ok, 100, "future field")
		msg, err := eventenvelope.Encode(resolvedHeader(), pair(ev))
		require.NoError(t, err)
		r, err := eventenvelope.Parse(msg)
		require.NoError(t, err)
		got, err := r.Member(0)
		require.NoError(t, err)
		assert.Equal(t, ev, got)
	})
	t.Run("a lower-numbered field after 17 and 18 is refused", func(t *testing.T) {
		for _, num := range []protowire.Number{1, 3, 9, 13, 16} {
			ev := appendField(ok, num, "late")
			_, err := eventenvelope.Encode(resolvedHeader(), pair(ev))
			assert.ErrorIs(t, err, eventenvelope.ErrBadInput, "field %d after the instants", num)
		}
	})
	t.Run("an unknown field below an interned one is refused", func(t *testing.T) {
		// Field 5 and 6 are reserved in PResolvedEvent; an event carrying one after 11 is out of order.
		ev := withEmptyDeviceType(t, ok)
		ev = appendField(ev, 5, "x")
		_, err := eventenvelope.Encode(resolvedHeader(), pair(ev))
		assert.ErrorIs(t, err, eventenvelope.ErrBadInput)
	})
	t.Run("a non-minimal tag on an interned field is refused", func(t *testing.T) {
		// Field 11 / bytes is 0x5a; 0xda 0x00 is the same tag in two bytes.
		var ev []byte
		rest := ok
		for len(rest) > 0 {
			num, typ, n := protowire.ConsumeTag(rest)
			vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
			if num == 11 {
				ev = append(ev, 0xda, 0x00)
				ev = append(ev, rest[n:n+vn]...)
			} else {
				ev = append(ev, rest[:n+vn]...)
			}
			rest = rest[n+vn:]
		}
		_, err := eventenvelope.Encode(resolvedHeader(), pair(ev))
		assert.ErrorIs(t, err, eventenvelope.ErrBadInput)
	})
	t.Run("a non-minimal length on an interned field is refused", func(t *testing.T) {
		var ev []byte
		rest := ok
		for len(rest) > 0 {
			num, typ, n := protowire.ConsumeTag(rest)
			vn := protowire.ConsumeFieldValue(num, typ, rest[n:])
			if num == 11 {
				val, _ := protowire.ConsumeBytes(rest[n:])
				ev = protowire.AppendTag(ev, 11, protowire.BytesType)
				ev = append(ev, byte(len(val))|0x80, 0x00) // the length in two bytes
				ev = append(ev, val...)
			} else {
				ev = append(ev, rest[:n+vn]...)
			}
			rest = rest[n+vn:]
		}
		_, err := eventenvelope.Encode(resolvedHeader(), pair(ev))
		assert.ErrorIs(t, err, eventenvelope.ErrBadInput)
	})
	t.Run("the control: the same event, canonical, is taken", func(t *testing.T) {
		_, err := eventenvelope.Encode(resolvedHeader(), pair(ok))
		assert.NoError(t, err)
	})
}

// Encode does not write what Parse refuses.
func TestEncodeDoesNotWriteWhatParseRefuses(t *testing.T) {
	t.Run("a dictionary that makes the message larger than any envelope may be", func(t *testing.T) {
		var events []eventenvelope.Event
		for i := 0; i < 64; i++ {
			e := loadtestResolved(i)
			e.DeviceTypeToken = fmt.Sprintf("%04d-%01000d", i, 0)
			e.ProfileVersionToken = fmt.Sprintf("%04d-%01000d", i, 1)
			events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: legacyBytes(t, e, eventtime.WireCompact)})
		}
		_, err := eventenvelope.Encode(resolvedHeader(), events)
		assert.ErrorIs(t, err, eventenvelope.ErrTooLarge)
	})
	t.Run("more than 64 settled indices, on both sides", func(t *testing.T) {
		h := resolvedHeader()
		for i := 0; i < 65; i++ {
			h.SettledElsewhere = append(h.SettledElsewhere, uint32(100+i))
		}
		_, err := eventenvelope.Encode(h, corpus(t, 3))
		assert.ErrorIs(t, err, eventenvelope.ErrTooManyMembers)

		var packed []byte
		for i := 0; i < 65; i++ {
			packed = protowire.AppendVarint(packed, uint64(100+i))
		}
		msg := seal(head(1, 2, "acme", bytesField(11, packed)))
		_, err = eventenvelope.Parse(msg)
		assert.ErrorIs(t, err, eventenvelope.ErrTooManyMembers)
		// 64 is the most.
		packed = packed[:64]
		_, err = eventenvelope.Parse(seal(head(1, 2, "acme", bytesField(11, packed))))
		assert.NoError(t, err)
	})
}

// The two ways of reading a member agree: a member whose bytes still carry a field the
// envelope interns is refused at Parse, so Member and the typed decoder never see it.
func TestAMemberStillCarryingAnInternedFieldIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		num  protowire.Number
		kind uint64
		wire protowire.Type
	}{
		{"device-type token", 11, 2, protowire.BytesType},
		{"profile token", 12, 2, protowire.BytesType},
		{"occurred", 17, 2, protowire.Fixed64Type},
		{"processed", 18, 2, protowire.Fixed64Type},
		{"occurred (unresolved)", 14, 1, protowire.Fixed64Type},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ev []byte
			ev = protowire.AppendTag(ev, 3, protowire.BytesType)
			ev = protowire.AppendString(ev, "dev")
			ev = protowire.AppendTag(ev, tc.num, tc.wire)
			if tc.wire == protowire.Fixed64Type {
				ev = protowire.AppendFixed64(ev, 7)
			} else {
				ev = protowire.AppendString(ev, "tok")
			}
			var rec []byte
			rec = protowire.AppendTag(rec, 6, protowire.BytesType)
			rec = protowire.AppendBytes(rec, ev)
			raw := protowire.AppendBytes(nil, rec)
			msg := seal(head(1, tc.kind, "acme", varint(9, 1), bytesField(10, raw), varint(12, uint64(len(raw)))))
			_, err := eventenvelope.Parse(msg)
			assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
		})
	}
}

// Typed decode and rebuild-then-legacy-decode give the same event for every member of every
// shape in the corpus, including ones with string instants and absent tokens.
func TestBothDecodersAgree(t *testing.T) {
	var events []eventenvelope.Event
	add := func(b []byte) { events = append(events, eventenvelope.Event{Index: uint32(len(events)), Bytes: b}) }
	add(legacyBytes(t, loadtestResolved(0), eventtime.WireLegacy))
	add(legacyBytes(t, loadtestResolved(1), eventtime.WireCompact))
	noTokens := loadtestResolved(2)
	noTokens.DeviceTypeToken, noTokens.ProfileVersionToken = "", ""
	add(legacyBytes(t, noTokens, eventtime.WireCompact))
	add(withEmptyDeviceType(t, legacyBytes(t, noTokens, eventtime.WireCompact)))
	early := loadtestResolved(3)
	early.OccurredTime = early.OccurredTime.Add(-time.Hour)
	add(legacyBytes(t, early, eventtime.WireCompact))
	msg, err := eventenvelope.Encode(resolvedHeader(), events)
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	for i := range events {
		legacy, err := r.Member(i)
		require.NoError(t, err)
		want, err := resolvedevent.UnmarshalResolvedEvent(legacy)
		require.NoError(t, err)
		got, err := resolvedevent.UnmarshalEnvelopeMember(r, i)
		require.NoError(t, err)
		assert.Equal(t, want, got, "member %d", i)
	}
}
