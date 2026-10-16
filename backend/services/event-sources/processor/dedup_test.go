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
	if want := "acme:9ix"; id != want {
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
		tenant, enc, ok := strings.Cut(id, ":")
		if !ok || tenant != "acme" {
			t.Fatalf("DedupID(%d) = %q: want the tenant before the first ':'", seq, id)
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
	if got, want := DedupID("acme", 123456789), "acme:21i3v9"; got != want {
		t.Errorf("DedupID = %q, want %q", got, want)
	}
}
