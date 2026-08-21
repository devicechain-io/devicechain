// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// 🔴 THIS FILE IS ABOUT THE OTHER PROCESS — THE ONE RUNNING BESIDE THIS ONE DURING AN UPGRADE.
//
// The pointer fact is distinguished by a FIELD, and a field means nothing to a decoder that
// predates it: encoding/json ignores keys it does not know, with no option here to refuse
// them. So an event-processing build from before FencesOmitted existed decodes a pointer fact
// as a fence set holding ZERO fences and installs it — containment then answers "outside" for
// a device that is inside, with nothing logged and nothing counted, because an empty fence set
// is a legitimate expected state. That is a REGRESSION in the exact dimension FencesOmitted
// exists to protect: before any of this, an oversized set was refused by the broker, the
// version stayed missing, and containment reported a COUNTED eval error the sweep repaired.
//
// device-management and event-processing roll as independent Deployments, so the mixed-version
// window opens on every upgrade and again on any rollback of one of them.
//
// The defence is the SUBJECT, not the field: pointers go to a stream an old build never
// subscribed to. These tests hold that line from both ends.

// oldConsumerDecode is what a build predating FencesOmitted did with a fence-set fact: a plain
// json.Unmarshal into a struct without the field, then "the fences in this message are the
// set". It is spelled out rather than imported because the whole point is to model code that
// no longer exists in this tree.
type oldConsumerFact struct {
	Version int32                         `json:"version"`
	Fences  []dmmodel.GeoFenceSnapshotRef `json:"fences"`
}

// An old decoder really does accept a pointer fact and read it as an empty fence set. This is
// the hazard, asserted rather than assumed — if it were ever false, the subject split would be
// unnecessary and this file should go.
func TestAnOldDecoderReadsAPointerFactAsAnEmptySet(t *testing.T) {
	pointer, err := dmmodel.MarshalGeoFenceSetMintedEvent(&dmmodel.GeoFenceSetMintedEvent{
		Version:       7,
		Fences:        []dmmodel.GeoFenceSnapshotRef{},
		FencesOmitted: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old oldConsumerFact
	if err := json.Unmarshal(pointer, &old); err != nil {
		t.Fatalf("an old decoder REFUSED the pointer fact (%v) — if that is now true by "+
			"construction, the subject split is redundant and this file is stale", err)
	}
	if old.Version != 7 {
		t.Errorf("old decoder read version %d, want 7", old.Version)
	}
	if len(old.Fences) != 0 {
		t.Errorf("old decoder read %d fences, want 0", len(old.Fences))
	}
	t.Logf("an old decoder accepts the pointer fact and sees version=%d fences=%d — which it "+
		"would install as the tenant's whole fence set", old.Version, len(old.Fences))
}

// 🔴 THE LINE THAT HOLDS: A POINTER FACT IS NEVER PUBLISHED WHERE AN OLD CONSUMER LISTENS.
//
// The fixture is the real one — device-management's own Api minting a fence set past the
// broker ceiling, through its real publisher — and the assertion is about WHICH writer
// received each fact. Asserting only "a pointer fact was produced" would pass against a
// publisher that put it on the ordinary subject, which is precisely the regression.
func TestPointerFactsNeverReachTheOrdinarySubject(t *testing.T) {
	_, facts, pointers := ceilingFenceSetSubjects(t, nil)

	if len(pointers.payloads) == 0 {
		t.Fatal("the fixture produced no pointer facts at all; it cannot say anything about " +
			"where they go")
	}
	for i, raw := range facts.payloads {
		ev, err := dmmodel.UnmarshalGeoFenceSetMintedEvent(raw)
		if err != nil {
			t.Fatalf("decode ordinary fact %d: %v", i, err)
		}
		if ev.FencesOmitted {
			t.Fatalf("ordinary-subject fact %d (version %d) is a POINTER — an event-processing "+
				"build from before this field existed installs it as an empty fence set, and "+
				"containment answers 'outside' for a device that is inside, silently",
				i, ev.Version)
		}
		if len(ev.Fences) == 0 {
			t.Errorf("ordinary-subject fact %d (version %d) carries no fences and is not marked "+
				"as a pointer; an empty set here means the tenant HAS no fences", i, ev.Version)
		}
	}
	// And the counterweight: everything on the pointer subject really is a pointer, so the
	// split is not just moving arbitrary traffic to a second stream.
	for i, raw := range pointers.payloads {
		ev, err := dmmodel.UnmarshalGeoFenceSetMintedEvent(raw)
		if err != nil {
			t.Fatalf("decode pointer fact %d: %v", i, err)
		}
		if !ev.FencesOmitted {
			t.Errorf("pointer-subject fact %d (version %d) is an ordinary fact; a new consumer "+
				"would resolve it from the archive for no reason", i, ev.Version)
		}
	}
	t.Logf("%d ordinary facts, %d pointer facts, and no pointer on the ordinary subject",
		len(facts.payloads), len(pointers.payloads))
}

// The two subjects are distinct stream suffixes, so a consumer subscribing to one does not
// receive the other. Without this the split is a naming convention rather than a boundary.
func TestTheTwoFenceSubjectsAreDistinct(t *testing.T) {
	if streams.GeoFenceSet == streams.GeoFenceSetPointer {
		t.Fatal("the pointer form shares the ordinary subject; an old consumer receives it")
	}
	seen := map[string]bool{}
	for _, s := range streams.All {
		if seen[s.Suffix] {
			t.Fatalf("stream suffix %q is declared twice", s.Suffix)
		}
		seen[s.Suffix] = true
	}
	if !seen[streams.GeoFenceSetPointer] {
		t.Errorf("%q is not declared in streams.All, so nothing creates it and every pointer "+
			"fact is published into the void", streams.GeoFenceSetPointer)
	}
}

// A new consumer DOES resolve a pointer fact arriving on the pointer subject — the other half
// of the split. A boundary that kept the fact away from old consumers and from new ones alike
// would pass the tests above and deliver nothing.
func TestANewConsumerResolvesAPointerFactFromThePointerSubject(t *testing.T) {
	api, _, pointers := ceilingFenceSetSubjects(t, nil)
	raw, ev := latestFact(t, pointers)

	src := &schemaFenceSource{t: t, api: api}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, src)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = src

	ack := &fakeAck{}
	// Delivered on the POINTER subject, which is the only place it is published.
	msg := fencePointerMsg("acme", raw, ack)
	if !rp.handleFenceSetFact(msg) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("a pointer fact on the pointer subject installed nothing")
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != ev.Version {
		t.Fatalf("the projection holds %v, want [%d]", held, ev.Version)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
}

// fencePointerMsg wraps a payload as a consumed message on the tenant's POINTER subject —
// the subject shape the consumer parses the tenant out of.
func fencePointerMsg(tenant string, payload []byte, ack messaging.Acknowledger) messaging.Message {
	return messaging.NewConsumedMessage("dc."+tenant+"."+streams.GeoFenceSetPointer, payload, 0, nil, ack)
}
