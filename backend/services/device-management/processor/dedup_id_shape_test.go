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
