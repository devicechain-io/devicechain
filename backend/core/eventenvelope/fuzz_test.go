// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"math/rand"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envelopes is the messages the corruption tests damage: both kinds, with and without
// compression, with and without a settlement record.
func envelopes(t testing.TB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	var err error
	out["resolved k=3 plain"], err = eventenvelope.Encode(resolvedHeader(), corpusOf(t, 3, loadtestResolved))
	require.NoError(t, err)
	out["resolved k=16 s2"], err = eventenvelope.Encode(resolvedHeader(), corpusOf(t, 16, realisticResolved))
	require.NoError(t, err)
	h := resolvedHeader()
	h.Round, h.SettledElsewhere = 2, []uint32{20, 21}
	out["resolved round 2 s2"], err = eventenvelope.Encode(h, corpusOf(t, 12, loadtestResolved))
	require.NoError(t, err)
	var ev []eventenvelope.Event
	for i := 0; i < 5; i++ {
		ev = append(ev, eventenvelope.Event{Index: uint32(i), Bytes: unresolvedBytes("mqtt", "dev", 1, []byte("payload-bytes"), nil, nil)})
	}
	out["unresolved k=5"], err = eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme"}, ev)
	require.NoError(t, err)
	return out
}

// membersOf reads every member of a parsed envelope as the legacy bytes.
func membersOf(t testing.TB, msg []byte) ([][]byte, error) {
	r, err := eventenvelope.Parse(msg)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for i := 0; i < r.Len(); i++ {
		m, err := r.Member(i)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Corruption is an error, never a different event. Every single-byte change, every bit flip
// and every truncation of every envelope is refused: there is no byte of a message that can
// change without the reader noticing.
func TestEveryCorruptionIsAnError(t *testing.T) {
	for name, msg := range envelopes(t) {
		t.Run(name, func(t *testing.T) {
			_, err := eventenvelope.Parse(msg)
			require.NoError(t, err, "the control")
			for i := range msg {
				for _, mutate := range []func(byte) byte{
					func(b byte) byte { return b ^ 0x01 },
					func(b byte) byte { return b ^ 0x80 },
					func(b byte) byte { return b + 1 },
					func(b byte) byte { return 0 },
				} {
					bad := append([]byte(nil), msg...)
					bad[i] = mutate(bad[i])
					if bad[i] == msg[i] {
						continue
					}
					_, err := eventenvelope.Parse(bad)
					require.Error(t, err, "byte %d of %d changed and still parses", i, len(msg))
				}
			}
			for n := 0; n < len(msg); n++ {
				_, err := eventenvelope.Parse(msg[:n])
				require.Error(t, err, "truncated to %d of %d bytes and still parses", n, len(msg))
			}
			_, err = eventenvelope.Parse(append(append([]byte(nil), msg...), 0))
			require.Error(t, err, "a trailing byte")
		})
	}
}

// FuzzParse: arbitrary bytes never panic, and whatever parses is self-consistent: it has no
// more than 64 members, every member rebuilds, and the rebuilt members encode back into an
// envelope that reads as the same members.
func FuzzParse(f *testing.F) {
	for _, msg := range envelopes(f) {
		f.Add(msg[:len(msg)-5]) // the body: the target seals it, so the fuzzer is not stopped at the checksum
	}
	f.Add([]byte{})
	f.Add([]byte{0x08, 0x01})
	f.Add([]byte{0x08, 0x01, 0x10, 0x02, 0x1a, 0x01, 0x61, 0x58, 0x01})
	f.Fuzz(func(t *testing.T, body []byte) {
		// The raw bytes must never panic; the sealed ones get past the checksum into the
		// parser, which is what the fuzzer is for.
		_, _ = eventenvelope.Parse(body)
		data := seal(body)
		r, err := eventenvelope.Parse(data)
		if err != nil {
			return
		}
		require.LessOrEqual(t, r.Len(), eventenvelope.MaxMembers)
		members, err := membersOf(t, data)
		if err != nil {
			return // a member whose bytes the rebuild refuses is refused, not mis-decoded
		}
		h := r.Header()
		var events []eventenvelope.Event
		for i, m := range members {
			events = append(events, eventenvelope.Event{Index: r.Index(i), Bytes: m})
		}
		again, err := eventenvelope.Encode(h, events)
		if err != nil {
			return // e.g. a one-member envelope that Encode would not write
		}
		back, err := membersOf(t, again)
		require.NoError(t, err)
		require.Equal(t, members, back)
	})
}

// FuzzRoundTrip: whatever events go in come out byte for byte, for random shapes of event,
// k, indices, time forms and settlement records.
func FuzzRoundTrip(f *testing.F) {
	for _, s := range []int64{1, 2, 3, 42, 1 << 40} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		rng := rand.New(rand.NewSource(seed))
		k := 2 + rng.Intn(eventenvelope.MaxMembers-1)
		format := eventtime.WireCompact
		if rng.Intn(4) == 0 {
			format = eventtime.WireLegacy
		}
		var events []eventenvelope.Event
		idx := uint32(0)
		for i := 0; i < k; i++ {
			e := realisticResolved(rng.Intn(1000))
			switch rng.Intn(6) {
			case 0:
				e.DeviceTypeToken, e.ProfileVersionToken = "", ""
			case 1:
				e.OccurredTime = e.OccurredTime.AddDate(-400, 0, 0) // string form
			case 2:
				e.ProcessedTime = e.ProcessedTime.AddDate(400, 0, 0)
			case 3:
				e.ExternalId = "ext-" + e.SourceDeviceToken
				e.GatewayToken = "gw-1"
			}
			b, err := resolvedevent.MarshalResolvedEventAs(e, format)
			require.NoError(t, err)
			idx += 1 + uint32(rng.Intn(3))
			events = append(events, eventenvelope.Event{Index: idx, Bytes: b})
		}
		h := resolvedHeader()
		h.SourceSeq = rng.Uint64()
		h.Round = uint32(rng.Intn(3))
		for i := 0; i < rng.Intn(4); i++ {
			h.SettledElsewhere = append(h.SettledElsewhere, idx+1+uint32(i))
		}
		msg, err := eventenvelope.Encode(h, events)
		if err != nil {
			require.ErrorIs(t, err, eventenvelope.ErrTooLarge)
			return
		}
		r, err := eventenvelope.Parse(msg)
		require.NoError(t, err)
		require.Equal(t, h.SettledElsewhere, r.Header().SettledElsewhere)
		require.Equal(t, h.SourceSeq, r.Header().SourceSeq)
		require.Equal(t, k, r.Len())
		for i := range events {
			require.Equal(t, events[i].Index, r.Index(i))
			got, err := r.Member(i)
			require.NoError(t, err)
			require.Equal(t, events[i].Bytes, got)
			want, err := resolvedevent.UnmarshalResolvedEvent(events[i].Bytes)
			require.NoError(t, err)
			typed, err := resolvedevent.UnmarshalEnvelopeMember(r, i)
			require.NoError(t, err)
			require.Equal(t, want, typed)
		}
		// And a corrupted copy is refused.
		bad := append([]byte(nil), msg...)
		bad[rng.Intn(len(bad))] ^= 1 << uint(rng.Intn(8))
		_, err = eventenvelope.Parse(bad)
		assert.Error(t, err)
	})
}
