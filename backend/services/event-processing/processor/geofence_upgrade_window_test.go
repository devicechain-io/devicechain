// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/streams"
)

// 🔴 THIS FILE IS ABOUT THE OTHER PROCESS — THE ONE RUNNING BESIDE THIS ONE DURING AN UPGRADE.
//
// A manifest entry is distinguished from a whole-set entry by carrying a HASH where the other
// carried a GEOMETRY, and a field means nothing to a decoder that predates it: encoding/json
// ignores keys it does not know and leaves the ones it wants at their zero values, with no
// option here to refuse them. So an event-processing build from before manifest delivery
// decodes a manifest as a fence set of N fences whose geometry is EMPTY, and installs it. That
// is not a decode failure — it is a fence set that is present, countable, and unevaluable.
//
// device-management and event-processing roll as independent Deployments, so the mixed-version
// window opens on every upgrade and again on any rollback of either of them.
//
// The defence is the SUBJECT: manifests go to a stream no earlier build ever subscribed to, so
// an old consumer's behaviour is exactly what it was before any of this existed — the fact does
// not arrive, the version stays missing, containment reports a COUNTED eval error, and the
// five-minute reconcile sweep repairs it. Degrading to a loud, already-handled failure beats
// degrading to a silent wrong answer. These tests hold that line from both ends.

// The subjects an event-processing build from BEFORE manifest delivery subscribed to.
//
// They are spelled as literals rather than imported because the whole point is to name code
// that no longer exists in this tree: streams.GeoFenceSet and streams.GeoFenceSetPointer were
// deleted by the cutover, so importing them is not available, and re-deriving them from
// anything still present would make this test agree with itself by construction.
const (
	retiredGeoFenceSetSubject        = "geofence-set"
	retiredGeoFenceSetPointerSubject = "geofence-set-pointer"
)

// oldConsumerFact is what a build predating manifest delivery did with a fence-set fact: a
// plain json.Unmarshal into a struct of version plus inlined fences, then "the fences in this
// message are the set". It is spelled out rather than imported for the same reason the subjects
// above are.
type oldConsumerFact struct {
	Version int32                         `json:"version"`
	Fences  []dmmodel.GeoFenceSnapshotRef `json:"fences"`
}

// An old decoder really does accept a manifest and read it as a fence set with no geometry.
// This is the hazard, asserted rather than assumed — if it were ever false, the subject split
// would be unnecessary and this file should go.
func TestAnOldDecoderReadsAManifestAsFencesWithNoGeometry(t *testing.T) {
	encoded, err := dmmodel.MarshalGeoFenceSetManifest(&dmmodel.GeoFenceSetManifest{
		Version: 7,
		Fences: []dmmodel.GeoFenceManifestEntry{
			{Token: "yard", Hash: "aa"}, {Token: "dock", Hash: "bb"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old oldConsumerFact
	if err := json.Unmarshal(encoded, &old); err != nil {
		t.Fatalf("an old decoder REFUSED the manifest (%v) — if that is now true by "+
			"construction, the subject split is redundant and this file is stale", err)
	}
	if old.Version != 7 {
		t.Errorf("old decoder read version %d, want 7", old.Version)
	}
	if len(old.Fences) != 2 {
		t.Fatalf("old decoder read %d fences, want 2", len(old.Fences))
	}
	for _, f := range old.Fences {
		if len(f.Geometry) != 0 {
			t.Errorf("old decoder read geometry %q for fence %q; if a manifest really did carry "+
				"geometry an old build could use, this whole file is unnecessary", f.Geometry, f.Token)
		}
	}
	t.Logf("an old decoder accepts the manifest and sees version=%d, %d fences, all with empty "+
		"geometry — which it would install as the tenant's whole fence set", old.Version, len(old.Fences))
}

// 🔴 THE LINE THAT HOLDS: THE MANIFEST SUBJECT IS NOT ONE AN OLD CONSUMER SUBSCRIBED TO.
//
// Both retired subjects were consumed by builds that decode a manifest into the empty-geometry
// fence set proved above, so reusing either name would deliver exactly that. The manifest's own
// suffix therefore has to differ from both — and it has to be a suffix the platform actually
// creates, or the fact is published into the void and every tenant's containment waits on the
// sweep forever.
func TestTheManifestSubjectIsNoneOfTheOnesAnOldConsumerSubscribedTo(t *testing.T) {
	for _, retired := range []string{retiredGeoFenceSetSubject, retiredGeoFenceSetPointerSubject} {
		if streams.GeoFenceSetManifest == retired {
			t.Fatalf("manifests are published to %q, a subject an event-processing build from "+
				"before manifest delivery subscribes to; it installs them as a fence set whose "+
				"every fence has empty geometry", retired)
		}
	}

	seen := map[string]bool{}
	for _, s := range streams.All {
		if seen[s.Suffix] {
			t.Fatalf("stream suffix %q is declared twice", s.Suffix)
		}
		seen[s.Suffix] = true
	}
	if !seen[streams.GeoFenceSetManifest] {
		t.Errorf("%q is not declared in streams.All, so nothing creates it and every fence-set "+
			"manifest is published into the void", streams.GeoFenceSetManifest)
	}
	// The counterweight: the retired subjects are GONE from the budget, not merely routed
	// around. A stream nothing publishes to and nothing consumes is a durable backlog waiting
	// to be redelivered into a decoder this tree no longer contains.
	for _, retired := range []string{retiredGeoFenceSetSubject, retiredGeoFenceSetPointerSubject} {
		if seen[retired] {
			t.Errorf("retired subject %q is still declared in streams.All; the cutover deleted "+
				"its producer and its consumer, so the platform is creating a stream nobody "+
				"reads or writes", retired)
		}
	}
}

// A new consumer DOES install a fact arriving on the manifest subject — the other half of the
// split. A boundary that kept the fact away from old consumers and from new ones alike would
// pass the tests above and deliver nothing.
//
// It goes through fenceFactMsg, which builds the per-tenant subject the running consumer parses
// its tenant out of — so this also asserts that the subject the producer targets is one this
// service can read a tenant from.
func TestANewConsumerInstallsAManifestFromTheManifestSubject(t *testing.T) {
	api, facts := ceilingFenceSet(t)
	raw, manifest := lastFact(t, facts)

	src := newSchemaFenceSource(t, api)
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	wireFenceArchive(rp, src)

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("a manifest fact on the manifest subject installed nothing")
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != manifest.Version {
		t.Fatalf("the projection holds %v, want [%d]", held, manifest.Version)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
}
