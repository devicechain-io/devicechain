// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package transport

import "testing"

// 🔴 THE VALUES ARE A WIRE CONTRACT, NOT AN IMPLEMENTATION DETAIL. Both are written into
// device_states.source and read back by the asserted-presence reconcilers through exact SQL
// equality, so renaming one strands every row already filed under the old name — there is no
// backfill. Pinned as literals so a rename has to come here and argue with this comment.
func TestMintedNamesAreTheOnesAlreadyInTheDatabase(t *testing.T) {
	if LwM2M != "lwm2m" {
		t.Errorf("LwM2M = %q, want %q", LwM2M, "lwm2m")
	}
	if Sparkplug != "sparkplug" {
		t.Errorf("Sparkplug = %q, want %q", Sparkplug, "sparkplug")
	}
}

// 🔴 THE COUNTERWEIGHT to the test above, and the more important of the two. A future author
// adding "mqtt" or "http" here would be adding a name NO PRODUCER EMITS — a plain gateway
// projects the operator's EventSource.Id, whose defaults are "mqtt1"/"http1" — and the whole
// package exists because a plausible-but-unemitted constant caused a live defect. Adding a
// genuinely minted transport means editing this test and saying why.
func TestMintedHoldsOnlyWhatThePlatformWrites(t *testing.T) {
	if len(Minted) != 2 {
		t.Fatalf("Minted has %d entries, want 2: %v", len(Minted), Minted)
	}
	for _, absent := range []Name{"mqtt", "http", "coap", ""} {
		if IsMinted(absent) {
			t.Errorf("IsMinted(%q) = true; a name nothing emits must not be reserved", absent)
		}
	}
}

func TestOfCutsTheQualifierOff(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   Name
	}{
		{"sparkplug:plant-a", Sparkplug},
		{"lwm2m", LwM2M},
		// A plain gateway's source is the operator's id, and Of does NOT promise to return
		// something minted — the caller decides membership.
		{"mqtt1", "mqtt1"},
		{"", ""},
		// 🔴 NOT A PREFIX MATCH. Both of these are reachable operator ids, and a prefix match
		// would classify them as Sparkplug and deny commands to devices that can receive them.
		{"sparkplugin", "sparkplugin"},
		{"sparkplug-test", "sparkplug-test"},
		// Cut at the FIRST colon: a qualifier may contain one.
		{"sparkplug:a:b", Sparkplug},
		// A trailing colon is a qualified form with an empty qualifier, not a bare name with
		// punctuation — it still classifies.
		{"sparkplug:", Sparkplug},
	} {
		if got := Of(tc.source); got != tc.want {
			t.Errorf("Of(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

// The round trip is the point of Qualify existing: what one side builds, the other side
// classifies. A separator written by hand on either side is how the two drift apart.
func TestQualifyRoundTripsThroughOf(t *testing.T) {
	for _, qualifier := range []string{"plant-a", "devicechain", "", "a:b"} {
		source := Qualify(Sparkplug, qualifier)
		if got := Of(source); got != Sparkplug {
			t.Errorf("Of(Qualify(Sparkplug, %q)) = %q, want %q", qualifier, got, Sparkplug)
		}
	}
	if got := Qualify(Sparkplug, "plant-a"); got != "sparkplug:plant-a" {
		t.Errorf("Qualify = %q, want the exact form sparkplug-ingest stamps", got)
	}
}

// The two predicates answer different questions, and the pair below is the whole reason
// StampedBare exists: both names are Minted, only one is stamped bare. A caller that used
// IsMinted where it meant IsStampedBare would treat "sparkplug" as a live collision with
// rows the platform writes — and no producer writes that value at all.
func TestStampedBareIsNarrowerThanMinted(t *testing.T) {
	if !IsMinted(Sparkplug) || !IsMinted(LwM2M) {
		t.Fatal("both names must be Minted, or this test is not testing the distinction it claims")
	}
	if !IsStampedBare(LwM2M) {
		t.Errorf("%q is stamped bare — lwm2m-ingest files presence under exactly that value", LwM2M)
	}
	if IsStampedBare(Sparkplug) {
		t.Errorf("%q is always qualified as %q; the bare word appears in no row a producer wrote",
			Sparkplug, Qualify(Sparkplug, "hostId"))
	}
}

// A name the platform mints nothing under is neither, so a caller cannot reach either
// branch with an operator's own id.
func TestIsStampedBareRejectsANameThePlatformNeverWrites(t *testing.T) {
	for _, name := range []Name{"mqtt", "http", "mqtt1", "sparkplug-test", ""} {
		if IsStampedBare(name) {
			t.Errorf("%q is not a value the platform stamps", name)
		}
	}
}
