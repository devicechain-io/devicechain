// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// TestSourceDedupIDIsCompact pins the dedup id's exact bytes. It rides every resolved event
// as its Nats-Msg-Id, stored on every replica, delivered to every consumer and held in each
// broker's duplicate table for the window, so it is kept short: a one-letter kind, the tenant,
// and the source sequence and fan-out index in base 36.
func TestSourceDedupIDIsCompact(t *testing.T) {
	for _, c := range []struct {
		kind  string
		seq   uint64
		index int
		want  string
	}{
		{dedupKindResolved, 1, 0, "r:tenant-0001:1:0"},
		{dedupKindResolved, 123456789, 1, "r:tenant-0001:21i3v9:1"},
		{dedupKindResolved, 36, 35, "r:tenant-0001:10:z"},
		{dedupKindResolved, math.MaxUint64, 36, "r:tenant-0001:3w5e11264sgsf:10"},
		{dedupKindFailed, 123456789, 0, "f:tenant-0001:21i3v9:0"},
	} {
		if got := sourceDedupID(c.kind, "tenant-0001", messaging.Message{StreamSeq: c.seq}, c.index); got != c.want {
			t.Errorf("sourceDedupID(%q, seq %d, index %d) = %q, want %q", c.kind, c.seq, c.index, got, c.want)
		}
	}
	if got := sourceDedupID(dedupKindResolved, "t", messaging.Message{}, 0); got != "" {
		t.Errorf("a source with no stream sequence got id %q, want none", got)
	}
}

// TestSourceDedupIDIsLossless is the dedup property itself. The id must be the SAME for a
// redelivery of one source (or the window cannot recognize the re-publish) and DIFFERENT for
// any two publishes (or the window silently discards a real event). Both follow from the id
// decoding back to exactly (kind, tenant, sequence, index): base 36 is a bijection, and no
// field can contain the ':' that separates them (a tenant is a token: letters, digits, '-'
// and '_'). A hashed or truncated id would fail the decode.
func TestSourceDedupIDIsLossless(t *testing.T) {
	type key struct {
		kind, tenant string
		seq          uint64
		index        int
	}
	seen := map[string]key{}
	for _, kind := range []string{dedupKindResolved, dedupKindFailed} {
		for _, tenant := range []string{"acme", "acme1", "a"} {
			for _, seq := range []uint64{1, 9, 10, 35, 36, 1295, 1296, 1 << 32, math.MaxUint64} {
				for _, index := range []int{0, 1, 35, 36, 255, math.MaxInt} {
					k := key{kind, tenant, seq, index}
					src := messaging.Message{StreamSeq: seq}
					id := sourceDedupID(kind, tenant, src, index)
					if again := sourceDedupID(kind, tenant, src, index); again != id {
						t.Fatalf("%+v: not deterministic: %q then %q", k, id, again)
					}
					parts := strings.Split(id, ":")
					if len(parts) != 4 {
						t.Fatalf("%+v: id %q does not have four ':'-separated fields", k, id)
					}
					s, err1 := strconv.ParseUint(parts[2], 36, 64)
					i, err2 := strconv.ParseInt(parts[3], 36, 64)
					if parts[0] != kind || parts[1] != tenant || err1 != nil || err2 != nil || s != seq || int(i) != index {
						t.Fatalf("%+v: id %q does not decode back to its source", k, id)
					}
					if prev, dup := seen[id]; dup {
						t.Fatalf("%+v and %+v share the id %q", prev, k, id)
					}
					seen[id] = k
				}
			}
		}
	}
}

// TestSourceDedupIDNeverMatchesAPreviousBuildsID is the rolling upgrade: both builds publish
// into resolved-events (and failed-events) while it runs, and the broker holds every id for
// the duplicate window. The previous build wrote "resolved:<tenant>:<seq>:<index>" and
// "failed:…" in DECIMAL, and every decimal string is also a base-36 one, so an id of this
// build that EQUALS one of those for a different source would be answered as a duplicate and
// the event discarded. The one-letter kinds are what keep the two shapes disjoint: a kind
// spelled out again would make this build's id for sequence 36 the old id for sequence 10.
func TestSourceDedupIDNeverMatchesAPreviousBuildsID(t *testing.T) {
	previous := func(kind, tenant string, seq uint64, index int) string {
		return kind + ":" + tenant + ":" + strconv.FormatUint(seq, 10) + ":" + strconv.Itoa(index)
	}
	for _, kind := range []string{dedupKindResolved, dedupKindFailed} {
		for seq := uint64(1); seq <= 50_000; seq++ {
			for _, index := range []int{0, 1, 36} {
				id := sourceDedupID(kind, "acme", messaging.Message{StreamSeq: seq}, index)
				parts := strings.Split(id, ":")
				if len(parts) != 4 {
					continue
				}
				oldSeq, err1 := strconv.ParseUint(parts[2], 10, 64)
				oldIndex, err2 := strconv.Atoi(parts[3])
				if err1 != nil || err2 != nil {
					continue
				}
				for _, oldKind := range []string{"resolved", "failed"} {
					if previous(oldKind, parts[1], oldSeq, oldIndex) == id {
						t.Fatalf("sourceDedupID(%q, seq %d, index %d) = %q, the previous build's id for %s "+
							"seq %d index %d: during an upgrade one of the two is discarded as a duplicate",
							kind, seq, index, id, oldKind, oldSeq, oldIndex)
					}
				}
			}
		}
	}
}
