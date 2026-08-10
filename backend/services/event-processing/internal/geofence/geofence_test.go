// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

// polygonFence builds a snapshot fence carrying a POLYGON_2D envelope over the given rings, each
// ring a list of [lon, lat] positions (closing position included, as GeoJSON requires).
func polygonFence(token string, rings ...[][2]float64) SnapshotFence {
	coords := make([][][2]float64, 0, len(rings))
	coords = append(coords, rings...)
	geom, err := json.Marshal(map[string]any{"type": "Polygon", "coordinates": coords})
	if err != nil {
		panic(err)
	}
	env, err := json.Marshal(map[string]any{"kind": KindPolygon2D, "geometry": json.RawMessage(geom)})
	if err != nil {
		panic(err)
	}
	return SnapshotFence{Token: token, Geometry: env}
}

// square is the closed ring of an axis-aligned lon/lat box.
func square(lonMin, latMin, lonMax, latMax float64) [][2]float64 {
	return [][2]float64{{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin}}
}

func at(lon, lat float64) Position { return Position{Lat: lat, Lon: lon} }

// mustContain asserts the containment answer for a token, failing on any error.
func mustContain(t *testing.T, fs *FenceSet, token string, p Position) bool {
	t.Helper()
	in, err := fs.Contains(token, p)
	if err != nil {
		t.Fatalf("Contains(%q, %+v): %v", token, p, err)
	}
	return in
}

// TestGeoFenceKindsMatchDeviceManagement pins the kind vocabulary against device-management's, the
// authority. The kind travels inside the stored envelope, so a rename there that is not mirrored
// here would make every fence of that kind fail to compile at evaluation — a runtime break with no
// compile-time signal, which is exactly what this test converts into one.
func TestGeoFenceKindsMatchDeviceManagement(t *testing.T) {
	for _, c := range []struct{ ours, theirs string }{
		{KindPolygon2D, dmmodel.GeoFenceKindPolygon2D},
		{KindPolygon25D, dmmodel.GeoFenceKindPolygon25D},
		{KindVoxel3D, dmmodel.GeoFenceKindVoxel3D},
	} {
		if c.ours != c.theirs {
			t.Errorf("kind %q does not match device-management's %q", c.ours, c.theirs)
		}
	}
}

// TestContainsInsideOutsideBoundary holds the stated convention: inside is inside, outside is
// outside, and EVERY point of the ring — mid-edge on all four edges, and a vertex — is INSIDE.
//
// The boundary half is not a formality. s2's own predicate splits the boundary between the two
// adjacent regions: measured on this very square it answers false at (0.5, 0.0) and true at
// (0.5, 1.0). A fence whose answer depends on which edge you are standing on is not a contract,
// so the mid-edge cases below are the ones that would fail without the explicit boundary test.
func TestContainsInsideOutsideBoundary(t *testing.T) {
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("yard", square(0, 0, 1, 1))})

	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("the centre of the fence is not inside it")
	}
	if mustContain(t, fs, "yard", at(2, 2)) {
		t.Error("a point well outside the fence is inside it")
	}
	for _, p := range []Position{
		at(0.5, 0), // south edge — s2 alone says false here
		at(0.5, 1), // north edge
		at(0, 0.5), // west edge
		at(1, 0.5), // east edge
		at(0, 0),   // a vertex
	} {
		if !mustContain(t, fs, "yard", p) {
			t.Errorf("boundary point %+v is not inside; the convention is boundary-INSIDE", p)
		}
	}
}

// TestContainsAcrossTheAntimeridian is one of the two cases planar maths gets silently wrong. The
// fence spans lon 179°E → 179°W: two degrees wide, straddling ±180. Planar point-in-polygon reads
// the same ring as a 358°-wide band covering the rest of the world and inverts BOTH answers below.
// S2 handles it by construction (an edge is the shorter geodesic), which is asserted here rather
// than assumed.
func TestContainsAcrossTheAntimeridian(t *testing.T) {
	// Authored as the wrap a real producer emits: 179 → -179, the short way round.
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("dateline", [][2]float64{
		{179, -1}, {-179, -1}, {-179, 1}, {179, 1}, {179, -1},
	})})

	if !mustContain(t, fs, "dateline", at(180, 0)) {
		t.Error("a point ON the antimeridian is outside the fence that straddles it (planar maths would say this)")
	}
	if !mustContain(t, fs, "dateline", at(-179.5, 0)) {
		t.Error("a point just west of the antimeridian is outside the fence")
	}
	if !mustContain(t, fs, "dateline", at(179.5, 0)) {
		t.Error("a point just east of the antimeridian is outside the fence")
	}
	if mustContain(t, fs, "dateline", at(0, 0)) {
		t.Error("null island is inside a 2-degree fence at the dateline (this is the planar answer)")
	}
	if mustContain(t, fs, "dateline", at(90, 0)) {
		t.Error("a point 90 degrees away is inside a 2-degree fence at the dateline")
	}
}

// TestContainsAtHighLatitude is the other case. The fence is authored as four corners of a
// "rectangle" lon[-10,10] × lat[80,81], but its southern edge is a GEODESIC, which bows poleward:
// at lon 0 that edge runs at lat ~80.15, not 80.
//
// The assertion that matters is the PAIR at the SAME latitude: (-9.9, 80.05) is inside and
// (0, 80.05) is outside. Planar maths calls both inside — they are between lat 80 and 81 — so no
// single-point test could distinguish the two implementations, and the disagreement at lon 0 is
// about 16 km. The third point is the positive control: well inside the box, inside both.
func TestContainsAtHighLatitude(t *testing.T) {
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("arctic", square(-10, 80, 10, 81))})

	if !mustContain(t, fs, "arctic", at(0, 80.5)) {
		t.Fatal("the control point well inside the fence is outside it; the fence is not what the test thinks")
	}
	if !mustContain(t, fs, "arctic", at(-9.9, 80.05)) {
		t.Error("(-9.9, 80.05) is outside; near the western edge the geodesic runs at ~80.003, so it is inside")
	}
	if mustContain(t, fs, "arctic", at(0, 80.05)) {
		t.Error("(0, 80.05) is inside; at lon 0 the southern geodesic runs at ~80.15, so it is OUTSIDE — " +
			"this is the planar answer, at the same latitude as the point above")
	}
}

// TestContainsWithHole: an interior ring excludes its interior, and the hole's OWN ring counts as
// inside — the boundary convention is uniform across every ring, because a hole's ring is part of
// the fence's boundary.
func TestContainsWithHole(t *testing.T) {
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("donut",
		square(0, 0, 10, 10),
		square(4, 4, 6, 6),
	)})

	if !mustContain(t, fs, "donut", at(1, 1)) {
		t.Error("a point in the ring of the donut is outside it")
	}
	if mustContain(t, fs, "donut", at(5, 5)) {
		t.Error("a point in the hole is inside the fence")
	}
	if !mustContain(t, fs, "donut", at(5, 4)) {
		t.Error("a point on the hole's ring is not inside; the fence's boundary includes its holes' rings")
	}
}

// TestContainsIgnoresRingWinding: RFC 7946's counterclockwise-exterior rule is advisory and real
// producers ignore it, so winding is normalized rather than trusted. Untrusted, a clockwise ring
// describes the COMPLEMENT — every point on Earth except the yard — which is the most dangerous
// possible failure because the fence still "works", inverted.
func TestContainsIgnoresRingWinding(t *testing.T) {
	cw := [][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}} // clockwise in lon/lat
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("cw", cw)})

	if !mustContain(t, fs, "cw", at(0.5, 0.5)) {
		t.Error("the centre is outside a clockwise-wound fence; winding was trusted rather than normalized")
	}
	if mustContain(t, fs, "cw", at(20, 20)) {
		t.Error("a distant point is inside a clockwise-wound fence; the fence was inverted")
	}
}

// TestUnknownFenceIsAnErrorNotFalse: a rule naming a fence the frozen set does not hold gets an
// ERROR. A false would read as a healthy, quiet rule and would cancel a Duration rule's hold.
func TestUnknownFenceIsAnErrorNotFalse(t *testing.T) {
	fs := NewFenceSet(3, []SnapshotFence{polygonFence("yard", square(0, 0, 1, 1))})

	in, err := fs.Contains("depot", at(0.5, 0.5))
	if err == nil {
		t.Fatalf("naming a fence that is not in the set returned (%v, nil); it must be an error", in)
	}
	if !errors.Is(err, ErrUnknownFence) {
		t.Errorf("error is %v, want ErrUnknownFence", err)
	}
	// Positive control in the same test: the set is real and answers the fence it DOES hold.
	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("the fence the set holds does not answer; the negative above proves nothing")
	}
}

// TestUnresolvableFenceSetIsDistinctFromEmpty holds the distinction the whole retention design
// rests on: "the fence set could not be resolved" (cannot know) is not "the tenant has no fences"
// (knowledge). Collapsing them is how a projection miss would come to read as a clean non-match.
func TestUnresolvableFenceSetIsDistinctFromEmpty(t *testing.T) {
	var missing *FenceSet
	if _, err := missing.Contains("yard", at(0, 0)); !errors.Is(err, ErrNoFenceSet) {
		t.Errorf("a nil fence set answered %v, want ErrNoFenceSet", err)
	}
	empty := EmptyFenceSet(0)
	_, err := empty.Contains("yard", at(0, 0))
	if !errors.Is(err, ErrUnknownFence) {
		t.Errorf("a known-empty fence set answered %v, want ErrUnknownFence", err)
	}
	if errors.Is(err, ErrNoFenceSet) {
		t.Error("a known-empty fence set reported the set as unavailable; empty is knowledge, not absence")
	}
}

// fakeKind is a geometry kind that exists only in this test. Registering it is how the dispatch is
// shown to be genuinely kind-agnostic: nothing in the containment path was written with it in mind,
// yet one Contains call evaluates it.
const fakeKind = "TEST_HALF_PLANE_2D"

// halfPlane is "inside iff longitude is negative" — deliberately nothing like a polygon, so it
// cannot accidentally be served by the 2D code path.
type halfPlane struct{}

func (halfPlane) contains(p Position) (bool, error) { return p.Lon < 0, nil }

// registerFakeKind installs the fake kind for the duration of a test.
func registerFakeKind(t *testing.T) {
	t.Helper()
	containment[fakeKind] = func(json.RawMessage) (geometry, error) { return halfPlane{}, nil }
	t.Cleanup(func() { delete(containment, fakeKind) })
}

// TestDispatchIsKindAgnostic: ONE containment call answers fences of DIFFERENT geometry kinds, and
// the caller never names a kind. This is the shape decision the arc is free to make only once —
// had containment forked into inFence/inVoxelRegion, upgrading a fence from 2D to 2.5D would be a
// RULE migration rather than a fence edit.
//
// Only 2D ships (device-management refuses to store the reserved kinds), so the second kind here is
// registered by the test. That is the point: a switch statement can only ever be tested with the
// kinds already in it, which would make "kind-agnostic" an assertion about today rather than about
// the shape.
func TestDispatchIsKindAgnostic(t *testing.T) {
	registerFakeKind(t)
	fakeEnvelope, err := json.Marshal(map[string]any{"kind": fakeKind, "geometry": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	fs := NewFenceSet(1, []SnapshotFence{
		polygonFence("yard", square(0, 0, 1, 1)),
		{Token: "west", Geometry: fakeEnvelope},
	})

	// The SAME call shape, against two different geometry kinds, answering by each kind's own rules.
	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("the polygon fence did not answer through the shared dispatch")
	}
	if mustContain(t, fs, "yard", at(-0.5, 0.5)) {
		t.Error("the polygon fence answered by the fake kind's rule")
	}
	if !mustContain(t, fs, "west", at(-0.5, 0.5)) {
		t.Error("the fake-kind fence did not answer through the shared dispatch")
	}
	if mustContain(t, fs, "west", at(0.5, 0.5)) {
		t.Error("the fake-kind fence answered by the polygon's rule")
	}
	if fs.Fence("west").Kind() != fakeKind || fs.Fence("yard").Kind() != KindPolygon2D {
		t.Errorf("kinds did not survive onto the fences: %q / %q", fs.Fence("west").Kind(), fs.Fence("yard").Kind())
	}
}

// TestReservedKindErrorsRatherThanAnsweringFalse: a kind with no evaluator (the reserved 2.5D and
// voxel kinds, or a kind from a newer device-management) is an error, never a false. Answering
// "not inside" for a shape nothing can evaluate is the one outcome indistinguishable from a
// correct negative.
func TestReservedKindErrorsRatherThanAnsweringFalse(t *testing.T) {
	for _, kind := range []string{KindPolygon25D, KindVoxel3D, "SOMETHING_FROM_THE_FUTURE"} {
		env, err := json.Marshal(map[string]any{"kind": kind, "geometry": json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		fs := NewFenceSet(1, []SnapshotFence{{Token: "f", Geometry: env}})
		in, err := fs.Contains("f", at(0, 0))
		if err == nil {
			t.Errorf("kind %q answered (%v, nil); an unevaluable kind must error", kind, in)
		}
	}
}

// TestOneBadFenceDoesNotDisableTheSet: a malformed fence is retained with its error rather than
// dropped (which would make it indistinguishable from one that was never authored) or fatal to the
// whole set (which would let one bad fence disable every other fence a tenant owns).
func TestOneBadFenceDoesNotDisableTheSet(t *testing.T) {
	fs := NewFenceSet(1, []SnapshotFence{
		{Token: "broken", Geometry: json.RawMessage(`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[0,0]]]}}`)},
		polygonFence("good", square(0, 0, 1, 1)),
	})
	if _, err := fs.Contains("broken", at(0.5, 0.5)); err == nil {
		t.Error("a fence with a degenerate ring answered without error")
	}
	if !mustContain(t, fs, "good", at(0.5, 0.5)) {
		t.Error("a sibling fence stopped working because another fence in the set was malformed")
	}
	if fs.Len() != 2 {
		t.Errorf("fence set holds %d fences, want 2 (the bad one is retained, not dropped)", fs.Len())
	}
}

// TestSelfIntersectingRingIsRefused: a bow-tie has no well-defined interior, so it errors rather
// than answering confidently.
//
// 🔴 The refusal is NOT s2's doing, and an earlier version of this comment said it was. s2's
// Loop.Validate passes a bow-tie — the Go port's crossing check is commented out upstream behind a
// TODO. What refuses it is core/geo's own scan, which device-management now runs at authoring time
// too, so this is no longer the first place an author finds out.
func TestSelfIntersectingRingIsRefused(t *testing.T) {
	bowtie := [][2]float64{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}}
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("bowtie", bowtie)})
	if in, err := fs.Contains("bowtie", at(0.5, 0.5)); err == nil {
		t.Errorf("a self-intersecting ring answered (%v, nil)", in)
	}
}

// TestNonFinitePositionIsRefused: a NaN position would convert to a garbage unit vector and get a
// confident answer out of it.
func TestNonFinitePositionIsRefused(t *testing.T) {
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("yard", square(0, 0, 1, 1))})
	nan := Position{Lat: 0.5, Lon: math.NaN()}
	if in, err := fs.Contains("yard", nan); err == nil {
		t.Errorf("a NaN longitude answered (%v, nil)", in)
	}
	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("the control point does not answer; the negative above proves nothing")
	}
}
