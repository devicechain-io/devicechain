// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dcconfig "github.com/devicechain-io/dc-microservice/config"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// These tests are about ONE tenant: the one that used geofencing exactly as documented.
// device-management admits MaxGeoFencesPerTenant fences of MaxGeoFenceVertices positions each,
// and a set at those limits does not fit through either path that can deliver it — not the
// broker's per-message ceiling, and not the cross-service client's response cap. Both are 1 MiB,
// for unrelated reasons, and the symptom of either was a counted containment eval error on every
// location event, forever.
//
// So the fixtures here build a fence set AT the documented ceiling and assert on its real byte
// size. A test whose fence set fits in 1 MiB would exercise none of this.

// maxVertexRing builds a valid closed GeoJSON polygon ring of exactly
// dmmodel.MaxGeoFenceVertices positions — a circle sampled at that resolution, so the ring is
// simple (non-self-intersecting) and passes device-management's real authoring validation rather
// than a relaxed test path.
//
// The coordinates are written at full float precision on purpose. The bytes are the point of
// these tests: a ring of 512 rounded two-decimal positions is a quarter the size of the ring a
// real author's editor produces, and sizing the fixture down would move the fence set back under
// the very cap it exists to cross.
func maxVertexRing(prec int, cx, cy, r float64) string {
	n := dmmodel.MaxGeoFenceVertices - 1 // the closing position repeats the first
	pos := fmt.Sprintf("[%%.%df,%%.%df]", prec, prec)
	var b strings.Builder
	b.WriteString("[[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		theta := 2 * math.Pi * float64(i) / float64(n)
		fmt.Fprintf(&b, pos, cx+r*math.Cos(theta), cy+r*math.Sin(theta))
	}
	// Close the ring on its first position, exactly.
	fmt.Fprintf(&b, ","+pos+"]]", cx+r*math.Cos(0), cy+r*math.Sin(0))
	return b.String()
}

// maxVertexFence wraps a max-vertex ring in the platform's stored geometry envelope.
//
// 🔴 THE PRECISION IS A PARAMETER BECAUSE THE SIZE IS THE VARIABLE, NOT THE VERTEX COUNT.
// A 100 × 512 fence set is ~670 KB at two decimal places and ~2.5 MB at twenty — the same
// fence count, the same position count, four times the bytes. Every fixture here having been
// written at one precision is how a page size chosen in FENCES could look adequate while
// being a guess about a quantity nobody was measuring.
func maxVertexFenceAt(prec int, cx, cy, r float64) string {
	return `{"kind":"` + geofence.KindPolygon2D + `","geometry":{"type":"Polygon","coordinates":` +
		maxVertexRing(prec, cx, cy, r) + `}}`
}

// maxVertexFence is the ordinary fixture: nine decimal places, about a millimetre at the
// equator and more precision than any real editor emits.
func maxVertexFence(cx, cy, r float64) string { return maxVertexFenceAt(9, cx, cy, r) }

// ceilingFenceSet seeds a tenant with the largest fence set device-management admits:
// MaxGeoFencesPerTenant fences of MaxGeoFenceVertices positions each, every one authored through
// the real CreateGeoFence (so the real vertex bound, the real count bound, the real version mint
// and the real snapshot freeze all run). One of them is token "yard" and contains the probe point
// the rules in these tests test, so the set is not merely large but evaluable.
//
// Only the LAST fact matters to the caller — it is the one whose set is at the ceiling — so the
// writer is handed the real broker ceiling and the facts accumulate as the fence set grows past it.
func ceilingFenceSet(t *testing.T, omitted prometheus.Counter) (*dmmodel.Api, *fenceFactWriter) {
	t.Helper()
	return ceilingFenceSetAt(t, 9, omitted)
}

// ceilingFenceSetAt is ceilingFenceSet at a chosen coordinate precision — the knob that moves
// the fence set's SIZE without moving its fence count or its vertex count.
func ceilingFenceSetAt(t *testing.T, prec int, omitted prometheus.Counter) (*dmmodel.Api, *fenceFactWriter) {
	t.Helper()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(context.Background(), "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts,
		dcconfig.DefaultStreamMaxMsgSize, omitted)

	// "yard" is a circle of radius 1 around the origin, so 0.5,0.5 is inside it (0.707 < 1).
	// The rest are disjoint circles parked far away, each also at the vertex ceiling.
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: maxVertexFenceAt(prec, 0, 0, 1)}); err != nil {
		t.Fatalf("create yard: %v", err)
	}
	for i := 1; i < dmmodel.MaxGeoFencesPerTenant; i++ {
		if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
			Token:    fmt.Sprintf("far-%03d", i),
			Geometry: maxVertexFenceAt(prec, float64(100+i%70), float64(i%80)-40, 0.25)}); err != nil {
			t.Fatalf("create far-%03d: %v", i, err)
		}
	}
	if len(facts.payloads) != dmmodel.MaxGeoFencesPerTenant {
		t.Fatalf("fixture: %d facts published, want %d (one per mint)",
			len(facts.payloads), dmmodel.MaxGeoFencesPerTenant)
	}
	return api, facts
}

// lastFact decodes the most recent published fact.
func lastFact(t *testing.T, facts *fenceFactWriter) ([]byte, *dmmodel.GeoFenceSetMintedEvent) {
	t.Helper()
	raw := facts.payloads[len(facts.payloads)-1]
	ev, err := dmmodel.UnmarshalGeoFenceSetMintedEvent(raw)
	if err != nil {
		t.Fatalf("decode the last fence-set fact: %v", err)
	}
	return raw, ev
}

// refusingFenceSource fails the test if anything asks it to resolve a fence set. It is how the
// counterweight test below proves the KNOWN-EMPTY set is installed from the fact itself and is
// never mistaken for a pointer fact — an assertion about an absent round trip cannot be made by
// checking a result, only by making the round trip impossible.
type refusingFenceSource struct{ t *testing.T }

func (s *refusingFenceSource) FenceSetAt(_ context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	s.t.Helper()
	s.t.Fatalf("the archive was consulted for %s version %d: a fence set that is genuinely EMPTY "+
		"was treated as a fact whose fences were omitted", tenant, version)
	return nil, nil
}

func (s *refusingFenceSource) CurrentFenceSet(_ context.Context, tenant string) (*geofence.FenceSet, error) {
	s.t.Helper()
	s.t.Fatalf("the archive's current-set door was consulted for %s during fact handling", tenant)
	return nil, nil
}

var (
	_ runtime.FenceSetSource        = (*refusingFenceSource)(nil)
	_ runtime.CurrentFenceSetSource = (*refusingFenceSource)(nil)
)

// ── 1. the oversized set, end to end ─────────────────────────────────────────────────────────

// A fence set too large for one broker message is published as a POINTER fact, and the engine
// resolves that pointer to the WHOLE set through the paged archive read — with nothing faked but
// the two transports.
//
// Every step is the shipped one: device-management's real Api mints the version and freezes the
// snapshot, its real publisher measures the marshalled fact against the real default broker
// ceiling and emits the pointer form, this service's real consumer recognizes it, the real paging
// client walks device-management's real GraphQL schema to reassemble the set, the real projection
// files it by version, and a real resolved location event fires a real compiled containment rule
// against it.
func TestOversizedFenceSetPublishesAPointerFactThatResolvesToTheWholeSet(t *testing.T) {
	ctx := context.Background()
	omitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_fence_facts_without_fences_total"})
	api, facts := ceilingFenceSet(t, omitted)

	raw, ev := lastFact(t, facts)

	// The premise, measured rather than assumed: this tenant's fence set really does not fit.
	full, err := dmmodel.MarshalGeoFenceSetMintedEvent(&dmmodel.GeoFenceSetMintedEvent{
		Version: ev.Version, Fences: fullSnapshotFences(t, api, ev.Version), MintedAt: ev.MintedAt})
	if err != nil {
		t.Fatalf("marshal the full fact: %v", err)
	}
	if len(full) <= int(dcconfig.DefaultStreamMaxMsgSize) {
		t.Fatalf("fixture is too small to test anything: the full fact is %d bytes, under the %d-byte "+
			"broker ceiling — this test would pass with the defect present",
			len(full), dcconfig.DefaultStreamMaxMsgSize)
	}
	t.Logf("fence set at the documented ceiling: %d fences, full fact %d bytes, pointer fact %d bytes",
		dmmodel.MaxGeoFencesPerTenant, len(full), len(raw))

	// The fact that was actually published is the POINTER form, and it fits.
	if !ev.FencesOmitted {
		t.Fatalf("a %d-byte fence set was published as an ordinary fact; the broker refuses it and "+
			"the version never reaches the engine", len(full))
	}
	if len(ev.Fences) != 0 {
		t.Errorf("a pointer fact carried %d fences, want 0", len(ev.Fences))
	}
	if len(raw) > int(dcconfig.DefaultStreamMaxMsgSize) {
		t.Errorf("the pointer fact is %d bytes, over the %d-byte ceiling it exists to fit under",
			len(raw), dcconfig.DefaultStreamMaxMsgSize)
	}
	if got := testutil.ToFloat64(omitted); got == 0 {
		t.Error("no fences-omitted fact was counted; an operator seeing containment eval errors has " +
			"nothing pointing at the cause")
	}

	// The consumer resolves the pointer through the paged archive read.
	src := &schemaFenceSource{t: t, api: api}
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, src)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = src

	// CONTROL: nothing is in the projection yet, so the rule cannot fire for any reason.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", ev.Version, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Fatalf("control: the rule fired with an empty projection (%d writes)", w.writes)
	}

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("the consumer marshalled no fence set onto the loop after a pointer fact")
	}
	if ack.acks != 1 {
		t.Errorf("the pointer fact was acked %d times, want 1", ack.acks)
	}

	// It went to the archive, at the version the fact named, and it took more than one page.
	if len(src.versionsAsked) == 0 {
		t.Fatal("the pointer fact was installed without reading the archive: whatever is in the " +
			"projection cannot be this version's fences")
	}
	for _, v := range src.versionsAsked {
		if v != ev.Version {
			t.Errorf("the archive was asked for version %d, want %d — the fact's version is the only "+
				"one whose set matches the events stamped with it", v, ev.Version)
		}
	}
	if src.pagesServed < 2 {
		t.Errorf("the whole set came back in %d response(s); the paging loop was not exercised",
			src.pagesServed)
	}
	if src.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("the largest single response was %d bytes, over the client's %d-byte read cap — "+
			"the page size does not actually solve the problem it was chosen for",
			src.largestBytes, svcclient.MaxResponseBytes)
	}

	// And what landed is the WHOLE set, not a page of it.
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != ev.Version {
		t.Fatalf("the projection holds versions %v, want [%d]", held, ev.Version)
	}

	// The rule fires: the device is inside the yard, which is one fence out of a hundred.
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", ev.Version, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("after resolving the pointer fact the containment rule published %d derived events, "+
			"want 1: the tenant's geofencing is still dead", w.writes)
	}
}

// fullSnapshotFences reads a version's frozen fences straight out of device-management's model, so
// a test can marshal the fact that WOULD have been published and measure it. It deliberately does
// not go through the GraphQL door — this is the producer-side size, not the reader-side one.
func fullSnapshotFences(t *testing.T, api *dmmodel.Api, version int32) []dmmodel.GeoFenceSnapshotRef {
	t.Helper()
	snap, err := api.GeoFenceSetSnapshotAt(dccore.WithTenant(context.Background(), "acme"), version)
	if err != nil {
		t.Fatalf("read snapshot %d: %v", version, err)
	}
	return snap.Fences
}

// ── 2. the counterweight: a genuinely empty set is NOT a pointer fact ─────────────────────────

// A fence set that is genuinely EMPTY stays empty, and is never resolved through the archive.
//
// 🔴 THIS IS THE TEST THAT MATTERS. "Fetch this yourself" and "this tenant has no fences" are both
// a fact with an empty fence list, and the second is a real, meaningful, correct state: a tenant
// that deleted its last fence sits at a non-zero version whose set is empty, and containment must
// answer "outside" from it. If the two were confused in the other direction a large fence set
// would install as an empty one — which reads downstream as a healthy rule that simply never
// fires, and is strictly worse than the publish failure this whole change replaces, because that
// one at least reported itself.
//
// The source used here FAILS the test on contact, so "it did not go to the archive" is asserted by
// construction rather than inferred from a result that happened to look right.
func TestKnownEmptyFenceSetIsNeverTreatedAsAPointerFact(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts,
		dcconfig.DefaultStreamMaxMsgSize, nil)

	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.DeleteGeoFence(dmCtx, "yard"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(facts.payloads) != 2 {
		t.Fatalf("fixture: %d facts, want 2 (create + delete)", len(facts.payloads))
	}

	raw, ev := lastFact(t, facts)
	if ev.FencesOmitted {
		t.Fatalf("the fact minted by deleting the last fence claims its fences were OMITTED; "+
			"an empty set is now indistinguishable from a set too big for the wire (payload %q)", raw)
	}
	if len(ev.Fences) != 0 {
		t.Fatalf("the fact after the delete carries %d fences, want 0", len(ev.Fences))
	}
	if ev.Version != 2 {
		t.Fatalf("the delete minted version %d, want 2", ev.Version)
	}

	refuse := &refusingFenceSource{t: t}
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, refuse)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = refuse

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("the known-empty set was not marshalled onto the loop")
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 2 {
		t.Fatalf("the projection holds versions %v, want [2] — the empty set is knowledge and must "+
			"be held, not absent", held)
	}

	// An event at the point the deleted fence used to contain evaluates to OUTSIDE — no derived
	// event, and no unresolvable-fence-set error either. The version is held; the answer is "no".
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Errorf("the rule fired against a fence set with no fences (%d writes)", w.writes)
	}
}

// The same distinction on the FETCH path: a tenant that has never had a fence resolves to the
// version-0 known-empty set, in one round trip and with no error. That is what its events' version
// stamp means, and the paging loop must not turn "totalRecords is zero" into a second request or
// into a failure.
func TestNeverFencedTenantResolvesToTheVersionZeroEmptySet(t *testing.T) {
	api := newFenceDmApi(t)
	src := &schemaFenceSource{t: t, api: api}

	set, err := src.CurrentFenceSet(context.Background(), "acme")
	if err != nil {
		t.Fatalf("CurrentFenceSet for a tenant with no fences: %v", err)
	}
	if set == nil {
		t.Fatal("a tenant with no fences resolved to a nil set; the caller turns that into " +
			"'unknowable', which is not what version 0 means")
	}
	if set.Version() != 0 {
		t.Errorf("the never-fenced tenant resolved to version %d, want 0", set.Version())
	}
	if n := set.Len(); n != 0 {
		t.Errorf("the never-fenced tenant resolved to %d fences, want 0", n)
	}
	if src.pagesServed != 1 {
		t.Errorf("resolving an empty set took %d responses, want 1", src.pagesServed)
	}
}

// ── 3. the paged fetch against the response cap ──────────────────────────────────────────────

// The paged read reassembles a fence set that a single response could not carry.
//
// The first half is the premise, measured on the REAL schema: asking for the whole set in one page
// produces a response larger than the cross-service client's read cap, i.e. the read that used to
// happen would have been refused. The second half is the fix: the shipped paging loop returns every
// fence, in order, with no page ever approaching the cap.
func TestPagedFenceSetFetchReassemblesASetLargerThanTheResponseCap(t *testing.T) {
	api, facts := ceilingFenceSet(t, nil)
	_, ev := lastFact(t, facts)

	// PREMISE: one response holding the whole set is REFUSED by the cap. Not "is large" —
	// refused, by the same error the production transport raises, so what follows is measured
	// against the wall rather than against an estimate of where the wall is.
	oneShot := &schemaFenceSource{t: t, api: api}
	var whole geoFenceSetSnapshotResponse
	err := oneShot.exec(context.Background(), "acme", geoFenceSetSnapshotQuery, map[string]any{
		"version":    ev.Version,
		"pagination": map[string]any{"pageNumber": 1, "pageSize": dmmodel.MaxGeoFencesPerTenant},
	}, &whole)
	if !errors.Is(err, svcclient.ErrResponseTooLarge) {
		t.Fatalf("a single response holding the whole set was accepted (%d bytes, err=%v); this "+
			"test exercises nothing the cap ever broke", oneShot.largestBytes, err)
	}
	t.Logf("whole-set response: %d bytes against a %d-byte read cap (refused)",
		oneShot.largestBytes, svcclient.MaxResponseBytes)

	// THE FIX: the shipped paging loop gets all of it, and no page comes close to the cap.
	paged := &schemaFenceSource{t: t, api: api}
	set, err := paged.FenceSetAt(context.Background(), "acme", ev.Version)
	if err != nil {
		t.Fatalf("paged FenceSetAt: %v", err)
	}
	if set.Version() != ev.Version {
		t.Errorf("the paged read produced version %d, want %d", set.Version(), ev.Version)
	}
	if n := set.Len(); n != dmmodel.MaxGeoFencesPerTenant {
		t.Fatalf("the paged read produced %d fences, want %d — a truncated fence set is "+
			"indistinguishable downstream from a small one", n, dmmodel.MaxGeoFencesPerTenant)
	}
	if paged.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("a page came back at %d bytes, over the %d-byte cap", paged.largestBytes, svcclient.MaxResponseBytes)
	}

	// Every fence the archive holds is present BY TOKEN. The count above cannot say that on its
	// own: a paging bug that served page 1 twice yields the right count and the wrong set.
	for _, f := range fullSnapshotFences(t, api, ev.Version) {
		if set.Fence(f.Token) == nil {
			t.Errorf("fence %q is missing from the reassembled set", f.Token)
		}
	}

	// The geometry survived the trip intact and still EVALUATES — paging must not lose or
	// re-encode the document containment is actually run against. Both directions, so a
	// fence that answers "inside" to everything cannot pass this.
	if in, err := set.Contains("yard", geofence.Position{Lat: 0.5, Lon: 0.5}); err != nil || !in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point within it", in, err)
	}
	if in, err := set.Contains("yard", geofence.Position{Lat: 40, Lon: 40}); err != nil || in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point far outside it", in, err)
	}
}
