// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
)

// wireSizeCases are header shapes the gate sizes, and the edges of the encoding: none, an
// empty map, many keys, several values for one key, a key with none, unicode, values the
// encoder trims or rewrites, and keys it refuses.
func wireSizeCases() map[string]*nats.Msg {
	many := nats.Header{}
	for i := 0; i < 40; i++ {
		many.Set("X-Key-"+strings.Repeat("k", i), strings.Repeat("v", i*3))
	}
	return map[string]*nats.Msg{
		"nil header":   {Subject: "a.b.c", Data: []byte("payload")},
		"empty header": {Subject: "a.b.c", Header: nats.Header{}, Data: []byte("payload")},
		"reply":        {Subject: "a.b.c", Reply: "_INBOX.x", Header: nats.Header{"K": {"v"}}},
		"no data":      {Subject: "a.b.c", Header: nats.Header{"K": {"v"}}},
		"natsMsg shape": natsMsg("dc.i.t.resolved-events", Message{
			Value: make([]byte, 300), DedupID: "dedup-1",
			Headers: map[string]string{HeaderCorrelationID: "cid", "dc-event-type": "measurement"},
		}),
		"many keys":       {Subject: "s", Header: many, Data: []byte{1}},
		"multi-value":     {Subject: "s", Header: nats.Header{"K": {"a", "bb", "", "ccc"}, "L": {"x"}}},
		"key no values":   {Subject: "s", Header: nats.Header{"K": {}, "L": nil}},
		"only empty key":  {Subject: "s", Header: nats.Header{"K": {}}},
		"empty value":     {Subject: "s", Header: nats.Header{"K": {""}}},
		"unicode value":   {Subject: "s", Header: nats.Header{"K": {"héllo wörld ✓ 日本語"}}},
		"unicode key":     {Subject: "s", Header: nats.Header{"Ké": {"v"}}},
		"trimmed value":   {Subject: "s", Header: nats.Header{"K": {" \t v a l \r\n "}}},
		"all whitespace":  {Subject: "s", Header: nats.Header{"K": {" \t\r\n"}}},
		"inner newlines":  {Subject: "s", Header: nats.Header{"K": {"a\r\nb\nc\rd"}}},
		"unicode space":   {Subject: "s", Header: nats.Header{"K": {" v　"}}},
		"invalid key":     {Subject: "s", Header: nats.Header{"Good": {"v"}, "Bad:Key": {"v"}}},
		"empty key":       {Subject: "s", Header: nats.Header{"": {"v"}}},
		"space in key":    {Subject: "s", Header: nats.Header{"A B": {"v"}}},
		"forbidden chars": {Subject: "s", Header: nats.Header{"a{b}": {"v"}}},
		"edge printables": {Subject: "s", Header: nats.Header{"!~#$%&'*+-.^_`|": {"v"}}},
	}
}

func TestWireSizeEqualsMsgSize(t *testing.T) {
	for name, nm := range wireSizeCases() {
		if got, want := wireSize(nm), nm.Size(); got != want {
			t.Errorf("%s: wireSize = %d, nats.Msg.Size = %d", name, got, want)
		}
	}
}

// TestWireSizeEveryKeyByte checks the key rule byte by byte: each of the 256 values alone,
// and inside an otherwise valid key, sizes as nats.go sizes it.
func TestWireSizeEveryKeyByte(t *testing.T) {
	for c := 0; c < 256; c++ {
		for _, k := range []string{string([]byte{byte(c)}), "Ab" + string([]byte{byte(c)}) + "cd"} {
			nm := &nats.Msg{Subject: "s", Header: nats.Header{k: {"v"}}}
			if got, want := wireSize(nm), nm.Size(); got != want {
				t.Errorf("key %q: wireSize = %d, nats.Msg.Size = %d", k, got, want)
			}
		}
	}
}

// TestWireSizeDoesNotEncode is why wireSize exists: nats.Msg.Size encodes the header block
// into a fresh buffer to measure it, and wireSize allocates nothing.
func TestWireSizeDoesNotEncode(t *testing.T) {
	nm := wireSizeCases()["natsMsg shape"]
	if allocs := testing.AllocsPerRun(100, func() { _ = wireSize(nm) }); allocs != 0 {
		t.Fatalf("wireSize allocates %v times per call; it must count, not encode", allocs)
	}
}

// FuzzWireSize compares wireSize with nats.Msg.Size on arbitrary subjects, replies, keys
// and values: a first key holding none to three of the values (shape picks how many, and
// whether that key is present at all), plus a second key.
func FuzzWireSize(f *testing.F) {
	f.Add("subj", "", "K", "v", "w", "L", "x", byte(2), []byte("data"))
	f.Add("s", "_INBOX.r", "Nats-Msg-Id", " trimmed\r\n", "", "dc-cid", "héllo", byte(3), []byte{})
	f.Add("s", "", "bad:key", "v", "w", "ok", "\t", byte(1), []byte{0})
	f.Add("s", "r", "K", "v", "w", "L", "x", byte(0), []byte{1})
	f.Add("s", "", "K", "v", "w", "L", "x", byte(4), []byte{1})
	f.Add("", "", "", "", "", "", "", byte(0), []byte(nil))
	f.Fuzz(func(t *testing.T, subject, reply, k1, v1, v2, k2, v3 string, shape byte, data []byte) {
		nm := &nats.Msg{Subject: subject, Reply: reply, Data: data, Header: nats.Header{}}
		if n := int(shape % 5); n < 4 {
			nm.Header[k1] = []string{v1, v2, v3}[:n]
		}
		if k2 != k1 {
			nm.Header[k2] = []string{v3}
		}
		if got, want := wireSize(nm), nm.Size(); got != want {
			t.Fatalf("wireSize = %d, nats.Msg.Size = %d for %#v", got, want, nm.Header)
		}
	})
}

// BenchmarkMsgSize compares the two ways of sizing the message natsMsg builds.
//
//	go test ./messaging -run '^$' -bench MsgSize -count 10
func BenchmarkMsgSize(b *testing.B) {
	nm := wireSizeCases()["natsMsg shape"]
	b.Run("nats.Msg.Size", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = nm.Size()
		}
	})
	b.Run("wireSize", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = wireSize(nm)
		}
	})
}
