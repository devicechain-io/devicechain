// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the golden envelopes in testdata from today's encoder")

// goldens are fixed inputs and the wire bytes they must produce. The bytes are the format:
// a producer of one release and a reader of another meet on them, and a stream can hold
// envelopes for days, so a change to what Encode writes for an input is a wire change and
// has to be deliberate.
//
// Two things are pinned for each file. READING: Parse of the stored bytes gives the stored
// members, which is the compatibility promise and does not depend on the encoder at all.
// WRITING: Encode of the input gives the stored bytes. The second also pins the s2 encoder's
// output for the compressed files, so a bump of that library that changes its bytes fails
// here; that is a reviewable regeneration (go test -run Golden -update), not a break, since
// the first test keeps proving every stored envelope still reads.
func goldens(t testing.TB) map[string]struct {
	header eventenvelope.Header
	events []eventenvelope.Event
} {
	type g = struct {
		header eventenvelope.Header
		events []eventenvelope.Event
	}
	out := map[string]g{}
	out["resolved_k16_s2"] = g{resolvedHeader(), corpusOf(t, 16, loadtestResolved)}
	out["resolved_k3_plain"] = g{resolvedHeader(), corpusOf(t, 3, loadtestResolved)}
	h := resolvedHeader()
	h.Round, h.SettledElsewhere = 1, []uint32{3, 9}
	late := corpusOf(t, 4, realisticResolved)
	for i := range late {
		late[i].Index = uint32(i * 2) // 0, 2, 4, 6: indices are the source's, not positions
	}
	out["resolved_round1_settled"] = g{h, late}
	var ev []eventenvelope.Event
	at := t0.UnixNano()
	for i := 0; i < 3; i++ {
		occ, proc := at+int64(i)*1_000_000, at+int64(i)*1_000_000+400_000
		ev = append(ev, eventenvelope.Event{Index: uint32(i), Bytes: unresolvedBytes("mqtt", "dev-"+string(rune('a'+i)), 1, []byte{9, 8, 7, byte(i)}, &occ, &proc)})
	}
	out["unresolved_k3_plain"] = g{eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme", SourceSeq: 77}, ev}
	return out
}

func goldenPath(name string) string { return filepath.Join("testdata", name+".hex") }

func TestGoldenEnvelopesReadAndWrite(t *testing.T) {
	for name, g := range goldens(t) {
		t.Run(name, func(t *testing.T) {
			msg, err := eventenvelope.Encode(g.header, g.events)
			require.NoError(t, err)
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(goldenPath(name), []byte(hex.EncodeToString(msg)+"\n"), 0o644))
			}
			raw, err := os.ReadFile(goldenPath(name))
			require.NoError(t, err, "no golden: run with -update")
			stored, err := hex.DecodeString(strings.TrimSpace(string(raw)))
			require.NoError(t, err)

			// READING: the stored bytes give the stored members, whatever the encoder does today.
			members, err := membersOf(t, stored)
			require.NoError(t, err)
			require.Len(t, members, len(g.events))
			r, err := eventenvelope.Parse(stored)
			require.NoError(t, err)
			assert.Equal(t, g.header, r.Header())
			for i, e := range g.events {
				assert.Equal(t, e.Bytes, members[i], "member %d", i)
				assert.Equal(t, e.Index, r.Index(i))
			}

			// WRITING: today's encoder writes the stored bytes.
			assert.Equal(t, hex.EncodeToString(stored), hex.EncodeToString(msg),
				"Encode no longer writes the pinned bytes; if this is deliberate, regenerate with -update and say so in the PR")
		})
	}
}

// The names of the header fields are on the wire too.
func TestMarkerIsPinned(t *testing.T) {
	assert.Equal(t, "Dc-Env", eventenvelope.HeaderName)
	assert.Equal(t, "1", eventenvelope.HeaderValue)
	assert.Equal(t, 64, eventenvelope.MaxMembers)
	assert.Equal(t, 65536, eventenvelope.MaxRawBytes)
	assert.Equal(t, 512, eventenvelope.CompressMin)
}
