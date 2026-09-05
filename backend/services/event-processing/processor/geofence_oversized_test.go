// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
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
	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// These tests are about ONE tenant: the one that used geofencing exactly as documented.
// device-management admits a full DEFAULT fence set — geoFenceCeiling fences of
// geoFencePositionCeiling positions each, the numbers a tenant that has declared nothing gets
// — and a set at those limits is over a megabyte, larger than the broker's per-message ceiling
// and larger than the cross-service client's response cap, both of which are 1 MiB for
// unrelated reasons. The symptom of hitting either was a counted containment eval error on
// every location event, forever.
//
// 🔴 WHAT THE FIXTURES MEASURE HAS CHANGED, AND THE CHANGE IS THE POINT OF THE ARC. When the
// fact carried the whole frozen set, the tenant's size was the ANNOUNCEMENT's problem, and this
// file was about a second form of the fact that existed to survive it. Manifest delivery makes
// the announcement's size a function of the fence COUNT alone, so that problem — and the second
// form, and every piece of machinery that chose between them — no longer exists. What survives
// is the half that was never about the fact: a fence set at these limits still cannot be
// TRANSFERRED in one response, and it must still resolve WHOLE and evaluate.
//
// So the fixtures still build a fence set AT the documented ceiling and still assert on its real
// byte size. A test whose fence set fits in 1 MiB would exercise none of this.
//
// 🔴 THE CEILINGS ARE THE DEFAULTS, NOT THE PLATFORM MAXIMA, AND THAT IS THE RIGHT CHOICE HERE
// RATHER THAN AN OVERSIGHT. They became tier settings when enforcement landed, so there are two
// numbers to pick from. These fixtures drive device-management's real Api with no caps resolver
// wired, which meters at the platform defaults — so the defaults are what the fence sets below
// are actually authored against, and using the maxima would build sets the Api refuses. What
// the maxima bound is a different question (what one BROKER MESSAGE must survive), asserted in
// device-management's own TestManifestFitsOneBrokerMessage.

// maxVertexRing builds a valid closed GeoJSON polygon ring at exactly the DEFAULT per-fence
// position ceiling — a circle sampled at that resolution, so the ring is
// simple (non-self-intersecting) and passes device-management's real authoring validation rather
// than a relaxed test path.
//
// The coordinates are written at full float precision on purpose. The bytes are the point of
// these tests: a ring of 512 rounded two-decimal positions is a quarter the size of the ring a
// real author's editor produces, and sizing the fixture down would move the fence set back under
// the very cap it exists to cross.
func maxVertexRing(prec int, cx, cy, r float64) string {
	n := governance.DefaultGeoFencePositionCeiling - 1 // the closing position repeats the first
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

// maxVertexFenceAt wraps a max-vertex ring in the platform's stored geometry envelope.
//
// 🔴 THE PRECISION IS A PARAMETER BECAUSE THE SIZE IS THE VARIABLE, NOT THE VERTEX COUNT.
// A 100 × 512 fence set is ~670 KB at two decimal places and ~2.5 MB at twenty — the same
// fence count, the same position count, four times the bytes. That is why the geometry batch is
// sized by arithmetic against a known PER-DOCUMENT byte ceiling rather than by a fence count,
// and a fixture written at a single precision could never show the difference.
func maxVertexFenceAt(prec int, cx, cy, r float64) string {
	return `{"kind":"` + geofence.KindPolygon2D + `","geometry":{"type":"Polygon","coordinates":` +
		maxVertexRing(prec, cx, cy, r) + `}}`
}

// maxVertexFence is the ordinary fixture: nine decimal places, about a millimetre at the
// equator and more precision than any real editor emits.
func maxVertexFence(cx, cy, r float64) string { return maxVertexFenceAt(9, cx, cy, r) }

// ceilingFenceSet seeds a tenant with the largest fence set device-management admits:
// a DEFAULT tenant's full fence set — geoFenceCeiling fences of geoFencePositionCeiling positions each, every one authored through
// the real CreateGeoFence (so the real vertex bound, the real count bound, the real version mint,
// the real snapshot freeze and the real geometry archiving all run). One of them is token "yard"
// and contains the probe point the rules in these tests test, so the set is not merely large but
// evaluable.
func ceilingFenceSet(t *testing.T) (*dmmodel.Api, *fenceFactWriter) {
	t.Helper()
	return ceilingFenceSetAt(t, 9)
}

// ceilingFenceSetAt is ceilingFenceSet at a chosen coordinate precision — the knob that moves
// the fence set's SIZE without moving its fence count or its vertex count.
func ceilingFenceSetAt(t *testing.T, prec int) (*dmmodel.Api, *fenceFactWriter) {
	t.Helper()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(context.Background(), "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, dcconfig.DefaultStreamMaxMsgSize, nil)

	// "yard" is a circle of radius 1 around the origin, so 0.5,0.5 is inside it (0.707 < 1).
	// The rest are disjoint circles parked far away, each also at the vertex ceiling.
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: maxVertexFenceAt(prec, 0, 0, 1)}); err != nil {
		t.Fatalf("create yard: %v", err)
	}
	for i := 1; i < governance.DefaultGeoFenceCeiling; i++ {
		if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
			Token:    fmt.Sprintf("far-%03d", i),
			Geometry: maxVertexFenceAt(prec, float64(100+i%70), float64(i%80)-40, 0.25)}); err != nil {
			t.Fatalf("create far-%03d: %v", i, err)
		}
	}
	if got := len(facts.payloads); got != governance.DefaultGeoFenceCeiling {
		t.Fatalf("fixture: %d facts published, want %d (one per mint)",
			got, governance.DefaultGeoFenceCeiling)
	}
	return api, facts
}

// fullSnapshotFences reads a version's frozen fences straight out of device-management's model,
// hydrated, so a test can measure how much geometry the version actually holds. It deliberately
// does not go through the manifest doors — this is the archive's own accounting, not the reader's.
func fullSnapshotFences(t *testing.T, api *dmmodel.Api, version int32) []dmmodel.GeoFenceSnapshotRef {
	t.Helper()
	snap, err := api.GeoFenceSetSnapshotAt(dccore.WithTenant(context.Background(), "acme"), version)
	if err != nil {
		t.Fatalf("read snapshot %d: %v", version, err)
	}
	return snap.Fences
}

// refusingTransport is a fenceSetExec that fails the test on contact. It is how the
// known-empty test below proves the empty set is installed from the manifest ITSELF — an
// assertion about an absent round trip cannot be made by checking a result, only by making the
// round trip impossible.
func refusingTransport(t *testing.T) fenceSetExec {
	return func(_ context.Context, tenant, query string, _ map[string]any, _ any) error {
		t.Errorf("the archive was consulted for tenant %q with query %q: a fence set that is "+
			"genuinely EMPTY was treated as one whose contents still had to be fetched",
			tenant, strings.SplitN(strings.TrimSpace(query), "\n", 2)[0])
		return fmt.Errorf("refusing transport")
	}
}

// ── 1. a fence set larger than one response, end to end ──────────────────────────────────────

// A fence set far too large to travel in one message or one response is announced by a TINY
// fact and resolves to the WHOLE set, with nothing faked but the two transports.
//
// Every step is the shipped one: device-management's real Api mints the version, freezes it as
// content references and archives each geometry; its real publisher marshals the manifest; this
// service's real consumer decodes it; the real client batches the geometry reads against the
// real GraphQL schema under the real response cap; the real projection files the result by
// version; and a real resolved location event fires a real compiled containment rule against it.
func TestAFenceSetLargerThanOneResponseIsAnnouncedByATinyFactAndResolvesWhole(t *testing.T) {
	ctx := context.Background()
	api, facts := ceilingFenceSet(t)
	raw, manifest := lastFact(t, facts)

	// PREMISE, measured rather than assumed: this tenant's geometry is over a megabyte, so no
	// single response could ever have carried it.
	stored := 0
	for _, f := range fullSnapshotFences(t, api, manifest.Version) {
		stored += len(f.Geometry)
	}
	if stored <= svcclient.MaxResponseBytes {
		t.Fatalf("fixture is too small to test anything: the version holds %d bytes of geometry, "+
			"inside the %d-byte response cap", stored, svcclient.MaxResponseBytes)
	}

	// 🔴 AND THE FACT THAT ANNOUNCES IT IS TINY. This is the property that deleted the pointer
	// fact, the headroom subtraction and the live max_payload clamp, so it is asserted on the
	// REAL published bytes rather than left as a claim in a comment: the announcement's size is a
	// function of the fence count and of nothing an author can write.
	if len(raw) > dmmodel.MaxGeoFenceSetManifestBytes() {
		t.Errorf("the manifest fact is %d bytes, over the %d-byte worst case its own arithmetic "+
			"promises", len(raw), dmmodel.MaxGeoFenceSetManifestBytes())
	}
	if len(raw) >= int(dcconfig.DefaultStreamMaxMsgSize) {
		t.Fatalf("the manifest fact is %d bytes against a %d-byte broker ceiling; the broker "+
			"refuses it and the version never reaches the engine at all",
			len(raw), dcconfig.DefaultStreamMaxMsgSize)
	}
	if len(manifest.Fences) != governance.DefaultGeoFenceCeiling {
		t.Fatalf("the manifest names %d fences, want %d — a short manifest is indistinguishable "+
			"downstream from a tenant with fewer fences", len(manifest.Fences), governance.DefaultGeoFenceCeiling)
	}
	t.Logf("fence set at the documented ceiling: %d fences, %d bytes of geometry, announced by a "+
		"%d-byte manifest fact", len(manifest.Fences), stored, len(raw))

	// The consumer resolves the manifest through the batched geometry read.
	src := newSchemaFenceSource(t, api)
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, nil)
	rp.fenceView = runtime.NewFenceSetView()
	wireFenceArchive(rp, src)

	// CONTROL: nothing is in the projection yet, so the rule cannot fire for any reason.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", manifest.Version, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Fatalf("control: the rule fired with an empty projection (%d writes)", w.writes)
	}

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if drainFenceUpdates(rp) != 1 {
		t.Fatal("the consumer marshalled no fence set onto the loop after a manifest fact")
	}
	if ack.acks != 1 {
		t.Errorf("the manifest fact was acked %d times, want 1", ack.acks)
	}

	// It resolved the geometry in BATCHES, and no response ever crossed the cap.
	st := src.stats()
	if st.geometryRequests < 2 {
		t.Errorf("the whole set's geometry came back in %d request(s); the chunking was not "+
			"exercised, so this test says nothing about a set larger than one response",
			st.geometryRequests)
	}
	for i, n := range st.chunkSizes {
		if n > dmmodel.MaxGeoFenceGeometryHashesPerRequest {
			t.Errorf("geometry request %d asked for %d addresses, over device-management's own "+
				"per-request limit of %d — the peer refuses it", i, n, dmmodel.MaxGeoFenceGeometryHashesPerRequest)
		}
	}
	if st.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("the largest single response was %d bytes, over the client's %d-byte read cap — "+
			"the chunk size does not actually solve the problem it was chosen for",
			st.largestBytes, svcclient.MaxResponseBytes)
	}
	// 🔴 THE MANIFEST FACT IS NOT RE-READ. The fact already names the version's fences, so
	// routing it through a version-addressed archive read would throw away the entire economy of
	// manifest delivery. One manifest read here would mean exactly that.
	if st.manifestReads != 0 {
		t.Errorf("resolving an ARRIVED manifest cost %d manifest reads, want 0: the fact already "+
			"says what to resolve", st.manifestReads)
	}

	// And what landed is the WHOLE set, not a batch of it.
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != manifest.Version {
		t.Fatalf("the projection holds versions %v, want [%d]", held, manifest.Version)
	}

	// The rule fires: the device is inside the yard, which is one fence out of a hundred.
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", manifest.Version, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("after resolving the manifest the containment rule published %d derived events, "+
			"want 1: the tenant's geofencing is still dead", w.writes)
	}
}

// ── 2. the counterweight: a genuinely empty set is not a set awaiting resolution ─────────────

// A fence set that is genuinely EMPTY stays empty, and never touches the archive.
//
// 🔴 THIS IS THE TEST THAT MATTERS, AND IT SURVIVES THE REDESIGN UNCHANGED IN SUBSTANCE. "This
// tenant has no fences" is a real, meaningful, correct state — a tenant that deleted its last
// fence sits at a non-zero version whose set is empty, and containment must answer "outside"
// from it. Confusing it with "there is something here I have not fetched yet" in either
// direction is a silent wrong answer: a large fence set installed as an empty one reads
// downstream as a healthy rule that simply never fires.
//
// The transport used here FAILS the test on contact, so "it did not go to the archive" is
// asserted by construction rather than inferred from a result that happened to look right.
func TestAKnownEmptyFenceSetIsInstalledWithoutTouchingTheArchive(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, dcconfig.DefaultStreamMaxMsgSize, nil)

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

	raw, manifest := lastFact(t, facts)
	if len(manifest.Fences) != 0 {
		t.Fatalf("the fact after the delete names %d fences, want 0", len(manifest.Fences))
	}
	if manifest.Version != 2 {
		t.Fatalf("the delete minted version %d, want 2", manifest.Version)
	}

	refusing := &schemaFenceSource{t: t, api: api}
	refusing.client = &fenceSetClient{
		transport: refusingTransport(t),
		cache:     geofence.NewGeometryCache(geofence.DefaultMaxCachedVertices),
	}
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, nil)
	rp.fenceView = runtime.NewFenceSetView()
	wireFenceArchive(rp, refusing)

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
// version-0 known-empty set, in ONE round trip and with no error. That is what its events'
// version stamp means, and an empty manifest must not become a geometry request for nothing or
// a failure.
func TestNeverFencedTenantResolvesToTheVersionZeroEmptySet(t *testing.T) {
	api := newFenceDmApi(t)
	src := newSchemaFenceSource(t, api)

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
	st := src.stats()
	if st.responses != 1 || st.manifestReads != 1 {
		t.Errorf("resolving an empty current set took %d responses (%d of them manifest reads), "+
			"want exactly 1 manifest read", st.responses, st.manifestReads)
	}
	if st.geometryRequests != 0 {
		t.Errorf("an empty manifest cost %d geometry requests, want 0", st.geometryRequests)
	}

	// The VERSION-ADDRESSED door answers version 0 with no round trip at all: version 0 is
	// knowledge, not a row, and asking the archive after it would be asking about a version that
	// was never minted.
	before := src.stats().responses
	zero, err := src.FenceSetAt(context.Background(), "acme", 0)
	if err != nil || zero == nil || zero.Version() != 0 || zero.Len() != 0 {
		t.Fatalf("FenceSetAt(0) = (%v, %v), want the known-empty version-0 set", zero, err)
	}
	if got := src.stats().responses - before; got != 0 {
		t.Errorf("resolving version 0 cost %d responses, want 0", got)
	}
}

// ── 3. the batched geometry read against the response cap ────────────────────────────────────

// The batched geometry read reassembles a fence set whose bodies a single response could not
// carry, and every fence still EVALUATES.
//
// The premise is measured on the REAL schema: the version's geometry is larger than the
// cross-service client's read cap, so the whole set was never going to arrive at once. The fix
// is the shipped chunking: every fence comes back, in bounded requests, with no response
// approaching the cap.
func TestTheGeometryBatchReadReassemblesASetLargerThanOneResponse(t *testing.T) {
	api, facts := ceilingFenceSet(t)
	_, manifest := lastFact(t, facts)

	src := newSchemaFenceSource(t, api)
	set, err := src.FenceSetAt(context.Background(), "acme", manifest.Version)
	if err != nil {
		t.Fatalf("FenceSetAt at the documented ceiling: %v", err)
	}
	if set.Version() != manifest.Version {
		t.Errorf("the read produced version %d, want %d", set.Version(), manifest.Version)
	}
	if n := set.Len(); n != governance.DefaultGeoFenceCeiling {
		t.Fatalf("the read produced %d fences, want %d — a truncated fence set is "+
			"indistinguishable downstream from a small one", n, governance.DefaultGeoFenceCeiling)
	}

	st := src.stats()
	if st.bytesServed <= svcclient.MaxResponseBytes {
		t.Fatalf("the whole read moved only %d bytes, inside the %d-byte cap — a single response "+
			"could have carried it and this test exercises nothing", st.bytesServed, svcclient.MaxResponseBytes)
	}
	if st.largestBytes > svcclient.MaxResponseBytes {
		t.Errorf("a response came back at %d bytes, over the %d-byte cap", st.largestBytes, svcclient.MaxResponseBytes)
	}
	if st.refusals != 0 {
		t.Errorf("%d responses were refused for size at the standard chunk size; the common case "+
			"is paying for splits it should not need", st.refusals)
	}
	// One manifest read and then geometry only. Reading the manifest twice would mean the version
	// was resolved through two questions about a moving target.
	if st.manifestReads != 1 {
		t.Errorf("the read spent %d manifest reads, want 1", st.manifestReads)
	}
	t.Logf("ceiling set: %d fences over %d responses (%d geometry batches, sizes %v), %d bytes "+
		"total, largest %d against a %d-byte cap", set.Len(), st.responses, st.geometryRequests,
		st.chunkSizes, st.bytesServed, st.largestBytes, svcclient.MaxResponseBytes)

	// Every fence the archive holds is present BY TOKEN. The count above cannot say that on its
	// own: a chunking bug that served one batch twice yields the right count and the wrong set.
	for _, f := range fullSnapshotFences(t, api, manifest.Version) {
		if set.Fence(f.Token) == nil {
			t.Errorf("fence %q is missing from the reassembled set", f.Token)
		}
	}

	// The geometry survived the trip intact and still EVALUATES — the batch read must not lose or
	// re-encode the document containment is actually run against. Both directions, so a fence
	// that answers "inside" to everything cannot pass this.
	if in, err := set.Contains("yard", geofence.Position{Lat: 0.5, Lon: 0.5}); err != nil || !in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point within it", in, err)
	}
	if in, err := set.Contains("yard", geofence.Position{Lat: 40, Lon: 40}); err != nil || in {
		t.Errorf("the reassembled yard reports inside=%v err=%v for a point far outside it", in, err)
	}
}

// ── 4. the economy the whole design exists for ───────────────────────────────────────────────

// Editing ONE fence of a hundred transfers ONE geometry body.
//
// 🔴 THIS IS THE CLAIM MANIFEST DELIVERY IS MADE OF, AND IT IS THE ONE PROPERTY NO CORRECTNESS
// TEST ABOVE CAN SEE. Every other test here passes just as well against a client that refetches
// all hundred bodies on every fence edit — same fences, same answers, same version, ~1.5 MB of
// cross-service traffic per edit instead of 15 KB. The bodies are content-addressed precisely so
// that the ninety-nine that did not change are already held, and the only way to observe that is
// to count what the second resolve ASKED FOR.
//
// The control is the first resolve in the same test, on the same cold source: it fetches
// everything. Without it, "one request of one address" would be satisfied by a client that had
// somehow fetched nothing at all.
func TestEditingOneFenceOfManyTransfersOnlyTheChangedBody(t *testing.T) {
	ctx := context.Background()
	api, facts := ceilingFenceSet(t)
	src := newSchemaFenceSource(t, api)

	// CONTROL: a cold resolve of the whole set fetches every distinct body.
	_, first := lastFact(t, facts)
	if _, err := src.ResolveManifest(ctx, "acme", first); err != nil {
		t.Fatalf("resolving the initial manifest: %v", err)
	}
	cold := src.stats()
	asked := 0
	for _, n := range cold.chunkSizes {
		asked += n
	}
	if asked != governance.DefaultGeoFenceCeiling {
		t.Fatalf("control: the cold resolve asked for %d addresses, want %d — if it did not fetch "+
			"the whole set, the comparison below means nothing", asked, governance.DefaultGeoFenceCeiling)
	}

	// Edit ONE fence. The mint re-freezes all hundred, so the new manifest names a hundred
	// addresses — ninety-nine of which are byte-identical to the ones already resolved.
	if _, err := api.UpdateGeoFence(dccore.WithTenant(ctx, "acme"), "yard",
		&dmmodel.GeoFenceUpdateRequest{Geometry: dcgraphql.OptionalStringOf(maxVertexFence(0, 0, 2))}); err != nil {
		t.Fatalf("update yard: %v", err)
	}
	_, second := lastFact(t, facts)
	if len(second.Fences) != governance.DefaultGeoFenceCeiling {
		t.Fatalf("the edit's manifest names %d fences, want %d", len(second.Fences), governance.DefaultGeoFenceCeiling)
	}

	set, err := src.ResolveManifest(ctx, "acme", second)
	if err != nil {
		t.Fatalf("resolving the edited manifest: %v", err)
	}
	warm := src.stats()
	newlyAsked := 0
	for _, n := range warm.chunkSizes[cold.geometryRequests:] {
		newlyAsked += n
	}
	if newlyAsked != 1 {
		t.Errorf("the edit transferred %d geometry bodies, want 1 (request sizes %v): the "+
			"content-addressed cache is not sparing the fences that did not change",
			newlyAsked, warm.chunkSizes[cold.geometryRequests:])
	}

	// And the edited set is still WHOLE and still right — an economy that dropped the ninety-nine
	// cached fences would be cheaper still and useless.
	if n := set.Len(); n != governance.DefaultGeoFenceCeiling {
		t.Fatalf("the edited set holds %d fences, want %d", n, governance.DefaultGeoFenceCeiling)
	}
	// The yard was enlarged from radius 1 to radius 2, so a point outside the old circle and
	// inside the new one is the discriminator: it proves the ONE body that was fetched is the
	// edited geometry rather than the cached one.
	if in, err := set.Contains("yard", geofence.Position{Lat: 1.2, Lon: 1.2}); err != nil || !in {
		t.Errorf("the edited yard reports inside=%v err=%v at (1.2,1.2), which the enlarged "+
			"circle contains and the original did not — the cache served the stale body", in, err)
	}
}
