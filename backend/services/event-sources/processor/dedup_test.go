// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strconv"
	"strings"
	"testing"
)

// The cross-tenant property, pinned on its own because its failure mode is silent
// DATA LOSS rather than a missed dedup: JetStream's dedup is stream-scoped, so two
// tenants sharing an id means one suppresses the other's event, the publish
// returns success, and nothing anywhere reports it.
func TestDedupIDIsTenantScoped(t *testing.T) {
	a, b := DedupID("acme", 42), DedupID("globex", 42)
	if a == "" || b == "" {
		t.Fatalf("expected ids for both tenants, got %q and %q", a, b)
	}
	if a == b {
		t.Errorf("tenants acme and globex share dedup id %q: one tenant's event would "+
			"silently suppress the other's", a)
	}
}

// The id must be stable across a redelivery — that is the entire point — and
// distinct for distinct captured messages.
func TestDedupIDIsStablePerCaptureSequence(t *testing.T) {
	if first, second := DedupID("acme", 77), DedupID("acme", 77); first != second {
		t.Errorf("the same capture sequence produced %q and %q: a redelivery would not "+
			"be recognized and dedup would silently do nothing", first, second)
	}
	if a, b := DedupID("acme", 77), DedupID("acme", 78); a == b {
		t.Errorf("distinct capture sequences share id %q: one message would be discarded", a)
	}
}

// No tenant, or no capture sequence, means NO id — never a weak one. An empty id
// publishes no header at all, so the write is simply not deduped, which is
// strictly safer than an id that could collide with another message.
func TestDedupIDReturnsEmptyWhenNoStableIdentityExists(t *testing.T) {
	cases := map[string]string{
		"no tenant":                         DedupID("", 5),
		"no capture sequence (HTTP ingest)": DedupID("acme", 0),
		"neither":                           DedupID("", 0),
	}
	for name, got := range cases {
		if got != "" {
			t.Errorf("%s: DedupID = %q, want \"\"", name, got)
		}
	}
}

// The id must contain nothing the DEVICE controls. This is the property that
// review found violated when the key was built from the device's alternate id:
// an unbounded device-chosen string went into a header the broker holds in memory
// for the whole duplicate window, two devices sharing a counter value destroyed
// each other's telemetry, and CR/LF normalization in header serialization let one
// device's id collide with another's on the wire.
//
// Asserting the id's SHAPE is what keeps that closed: anything device-derived
// would have to widen this.
func TestDedupIDContainsNothingDeviceControlled(t *testing.T) {
	const tenant = "acme"
	id := DedupID(tenant, 12345)
	if want := "acme.9ix"; id != want {
		t.Errorf("DedupID = %q, want %q — the id is the tenant and the capture sequence (base 36), "+
			"and nothing else", id, want)
	}
	// Bounded length follows from that exact shape, and bounded length is what stops
	// a device turning the broker's duplicate window into gigabytes of memory. There
	// is deliberately no separate length assertion: with the id pinned exactly above,
	// a `len(id) < N` check could never fire, and a check that cannot fail reads as
	// coverage without being any.
}

// The compact id is still the capture sequence, losslessly: base 36 is a bijection on the
// sequence, so two captured messages can never share an id (no silent suppression) and a
// redelivery of one always reproduces its id exactly (the duplicate window recognizes it).
// Decoding it back is the proof of both; a hash or a truncation would fail it.
func TestDedupIDIsTheCaptureSequenceLosslessly(t *testing.T) {
	seen := map[string]uint64{}
	for _, seq := range []uint64{1, 9, 10, 35, 36, 1295, 1296, 123456789, 1 << 32, 1<<63 - 1, 1 << 63, ^uint64(0)} {
		id := DedupID("acme", seq)
		if again := DedupID("acme", seq); again != id {
			t.Fatalf("DedupID(%d) is not deterministic: %q then %q", seq, id, again)
		}
		tenant, enc, ok := strings.Cut(id, ".")
		if !ok || tenant != "acme" {
			t.Fatalf("DedupID(%d) = %q: want the tenant before the first '.'", seq, id)
		}
		got, err := strconv.ParseUint(enc, 36, 64)
		if err != nil || got != seq {
			t.Fatalf("DedupID(%d) = %q decodes to %d (%v), want the sequence back", seq, id, got, err)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("sequences %d and %d share the id %q", prev, seq, id)
		}
		seen[id] = seq
	}
	if got, want := DedupID("acme", 123456789), "acme.21i3v9"; got != want {
		t.Errorf("DedupID = %q, want %q", got, want)
	}
}

// previousBuildDedupID is the id the build before base 36 published for a capture message:
// the tenant, ':' and the sequence in DECIMAL. It is frozen here, not shared with DedupID,
// because it is what a broker may still be holding from a pod of that build.
func previousBuildDedupID(tenant string, captureSeq uint64) string {
	return tenant + ":" + strconv.FormatUint(captureSeq, 10)
}

// TestDedupIDNeverMatchesAPreviousBuildsID is the rolling upgrade. Both builds publish into
// the same inbound-events stream while the upgrade runs, and the broker holds every id for
// the duplicate window, so an id this build publishes that EQUALS one the previous build
// published for a different capture message is not a missed dedup: the broker answers it
// as a duplicate and the event is silently discarded.
//
// Every decimal string is also a base-36 one, so with the same "<tenant>:" in front the two
// shapes would overlap: the previous build's "acme:1000" (sequence 1,000) would be this
// build's id for sequence 46,656. On a young stream — a fresh install upgraded within
// minutes — both sequences fall inside one window. So the shapes must be DISJOINT, not
// merely different, which is what the '.' separator buys.
func TestDedupIDNeverMatchesAPreviousBuildsID(t *testing.T) {
	const tenant = "acme"
	check := func(seq uint64) {
		id := DedupID(tenant, seq)
		// Read the id as the previous build would have written it, for ANY tenant.
		i := strings.LastIndexByte(id, ':')
		if i < 0 {
			return // the previous build's ids all contain ':'
		}
		owner, rest := id[:i], id[i+1:]
		old, err := strconv.ParseUint(rest, 10, 64)
		if err != nil || previousBuildDedupID(owner, old) != id {
			return
		}
		// The SAME message under both builds' ids is a correct dedup, not a collision.
		if owner == tenant && old == seq {
			return
		}
		t.Fatalf("DedupID(%q, %d) = %q, the id the previous build published for tenant %q's capture "+
			"sequence %d: during an upgrade one of the two events is discarded as a duplicate",
			tenant, seq, id, owner, old)
	}
	for seq := uint64(1); seq <= 200_000; seq++ {
		check(seq)
	}
	for _, seq := range []uint64{1_679_616, 60_466_176, 1 << 40, ^uint64(0)} {
		check(seq)
	}
}

// captureSeqOf reads the capture sequence back out of a capture dedup id (DedupID), 0 for an
// empty one, for a test that wants to see which captured message an event was built from.
func captureSeqOf(id string) uint64 {
	i := strings.LastIndexByte(id, '.')
	if i < 0 {
		return 0
	}
	n, _ := strconv.ParseUint(id[i+1:], 36, 64)
	return n
}
