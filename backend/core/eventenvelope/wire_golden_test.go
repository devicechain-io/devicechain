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
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
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

	// Every instant as a string, as a producer on the legacy wire format writes them: both
	// member flags set, no numeric instant at all, so the base time is absent.
	var legacyFmt []eventenvelope.Event
	for i := 0; i < 3; i++ {
		legacyFmt = append(legacyFmt, eventenvelope.Event{Index: uint32(i), Bytes: legacyBytes(t, loadtestResolved(i), eventtime.WireLegacy)})
	}
	out["resolved_string_instants_no_base"] = g{resolvedHeader(), legacyFmt}

	// Tokens absent on some members, present-but-empty on one, a later member EARLIER than the
	// first (negative deltas), and one member with a string instant among numeric ones.
	noTokens := loadtestResolved(1)
	noTokens.DeviceTypeToken, noTokens.ProfileVersionToken = "", ""
	early := loadtestResolved(2)
	early.OccurredTime = early.OccurredTime.Add(-90 * time.Second)
	early.ProcessedTime = early.OccurredTime.Add(-time.Second)
	stringInstant := loadtestResolved(3)
	stringInstant.OccurredTime = time.Date(1500, 1, 2, 3, 4, 5, 6, time.UTC)
	mixed := []eventenvelope.Event{
		{Index: 0, Bytes: legacyBytes(t, loadtestResolved(0), eventtime.WireCompact)},
		{Index: 1, Bytes: legacyBytes(t, noTokens, eventtime.WireCompact)},
		{Index: 2, Bytes: withEmptyDeviceType(t, legacyBytes(t, noTokens, eventtime.WireCompact))},
		{Index: 3, Bytes: legacyBytes(t, early, eventtime.WireCompact)},
		{Index: 5, Bytes: legacyBytes(t, stringInstant, eventtime.WireCompact)},
	}
	out["resolved_absent_empty_refs_negative_deltas"] = g{resolvedHeader(), mixed}

	// A settlement record that carries no members at all.
	hs := resolvedHeader()
	hs.Round, hs.SettledElsewhere = 2, []uint32{0, 1, 2, 7}
	out["resolved_settlement_only"] = g{hs, nil}

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
