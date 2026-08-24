// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"
)

// polygonDocument builds the stored POLYGON_2D geometry envelope for the given rings — the same
// document shape device-management archives under a content address, without the fence token a
// snapshot wraps it in.
func polygonDocument(rings ...[][2]float64) []byte {
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
	return env
}

// countedKind is a second fake geometry kind, distinct from halfPlane in exactly one way: it
// implements compiledDetail, and records every call. It is how "the vertex count came from the
// geometry" and "somebody pre-built the index, and did it before publishing the value" become
// observed calls rather than inferences.
//
// It is used only from single-goroutine tests, so its recorder needs no synchronization; the
// concurrency tests use real polygons.
const countedKind = "TEST_COUNTED_2D"

// countedKindRecorder collects what the fake geometry was asked to do. onPrebuild, when set, runs
// inside prebuildIndex — which is how a test observes the state of the world AT the moment of the
// pre-build rather than after it.
type countedKindRecorder struct {
	prebuilds  int
	counts     int
	onPrebuild func()
}

type countedGeometry struct {
	vertices int
	rec      *countedKindRecorder
}

func (g countedGeometry) contains(p Position) (bool, error) { return p.Lon < 0, nil }

func (g countedGeometry) vertexCount() int {
	g.rec.counts++
	return g.vertices
}

func (g countedGeometry) prebuildIndex() {
	g.rec.prebuilds++
	if g.rec.onPrebuild != nil {
		g.rec.onPrebuild()
	}
}

// registerCountedKind installs the counting fake for the duration of a test. Its compiled geometry
// reports whatever vertex count the document names, so a test can mint documents of any cost.
func registerCountedKind(t *testing.T) *countedKindRecorder {
	t.Helper()
	rec := &countedKindRecorder{}
	containment[countedKind] = func(raw json.RawMessage) (geometry, error) {
		var body struct {
			Vertices int `json:"vertices"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		return countedGeometry{vertices: body.Vertices, rec: rec}, nil
	}
	t.Cleanup(func() { delete(containment, countedKind) })
	return rec
}

// countedDocument is a stored envelope declaring countedKind, of the given cost. marker only makes
// two documents of the same cost differ in bytes, and therefore in content address.
func countedDocument(marker string, vertices int) []byte {
	body, err := json.Marshal(map[string]any{"marker": marker, "vertices": vertices})
	if err != nil {
		panic(err)
	}
	env, err := json.Marshal(map[string]any{"kind": countedKind, "geometry": json.RawMessage(body)})
	if err != nil {
		panic(err)
	}
	return env
}

// TestCompileGeometryCarriesBothTheKindAndTheShape is the reason Compiled is a pair rather than a
// bare geometry: a fence assembled from the compiled value must still be able to say what it is.
func TestCompileGeometryCarriesBothTheKindAndTheShape(t *testing.T) {
	c, err := CompileGeometry(polygonDocument(square(0, 0, 1, 1)))
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	if c.Kind() != KindPolygon2D {
		t.Errorf("Kind() = %q, want %q", c.Kind(), KindPolygon2D)
	}
	f := NewCompiledFence("yard", c)
	if f.Kind() != KindPolygon2D {
		t.Errorf("the kind did not survive onto the fence: %q", f.Kind())
	}
	if f.Token() != "yard" {
		t.Errorf("Token() = %q, want %q", f.Token(), "yard")
	}
	fs := &FenceSet{version: 7, byToken: map[string]*Fence{"yard": f}}
	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("a fence built from an already-compiled geometry did not answer containment")
	}
	if mustContain(t, fs, "yard", at(5, 5)) {
		t.Error("a fence built from an already-compiled geometry answered inside for a point outside it")
	}
}

// TestOneCompiledGeometryIsSharedByEveryFenceBuiltFromIt: reuse is the entire purpose of the
// exported surface, so the shape it produces has to actually be shared rather than deep-copied —
// otherwise a cache would save the parse and the compile but not the memory, and the vertex bound
// it is measured by would be counting something nobody holds.
func TestOneCompiledGeometryIsSharedByEveryFenceBuiltFromIt(t *testing.T) {
	c, err := CompileGeometry(polygonDocument(square(0, 0, 1, 1)))
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	a, b := NewCompiledFence("gate-a", c), NewCompiledFence("gate-b", c)
	if a.geom != b.geom {
		t.Error("two fences built from one Compiled hold different geometries; nothing is being shared")
	}
}

// TestCompileGeometryCountsEveryRing: the vertex count is the cache's cost unit, so it has to be
// the whole geometry's, not the exterior ring's. A count that ignored holes would price a
// doughnut-shaped fence as its outline and let a cache hold twice what its bound says.
//
// It also pins the COMPILED-vertex convention: a GeoJSON ring repeats its first position to close
// itself and the compiler drops that duplicate, so a 5-position square is 4 vertices here.
func TestCompileGeometryCountsEveryRing(t *testing.T) {
	exterior := square(0, 0, 10, 10)
	hole := square(4, 4, 6, 6)

	solid, err := CompileGeometry(polygonDocument(exterior))
	if err != nil {
		t.Fatalf("CompileGeometry(solid): %v", err)
	}
	if solid.Vertices() != 4 {
		t.Errorf("a 5-position square compiled to %d vertices, want 4 (the closing position is dropped)", solid.Vertices())
	}

	withHole, err := CompileGeometry(polygonDocument(exterior, hole))
	if err != nil {
		t.Fatalf("CompileGeometry(with hole): %v", err)
	}
	if withHole.Vertices() != 8 {
		t.Errorf("a square with a square hole counted %d vertices, want 8 (the hole was not counted)", withHole.Vertices())
	}
}

// TestVerticesComeFromTheGeometryItself: the count must be asked of the compiled shape, not
// guessed from the document. A count derived from the JSON would be right for POLYGON_2D and wrong
// for every kind that lands after it, and would be wrong silently.
func TestVerticesComeFromTheGeometryItself(t *testing.T) {
	rec := registerCountedKind(t)
	c, err := CompileGeometry(countedDocument("x", 37))
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	if got := c.Vertices(); got != 37 {
		t.Errorf("Vertices() = %d, want 37 (the geometry's own answer)", got)
	}
	if rec.counts == 0 {
		t.Error("Vertices() never asked the geometry; the count came from somewhere else")
	}
}

// TestEveryRegisteredKindIsCountable is the gate that keeps the OPTIONAL half of the geometry
// contract from being optional in practice. A kind that ships without a vertex count is charged
// the floor instead — a 40,000-vertex coastline priced at 1 — which makes a cache's bound stop
// bounding anything, quietly, from the moment the kind is registered.
//
// It walks the live dispatch table, so adding a kind and forgetting fails here rather than in
// production. A new kind needs a sample document added below; that is the reminder, and it is
// cheaper than the alternative.
func TestEveryRegisteredKindIsCountable(t *testing.T) {
	samples := map[string][]byte{
		KindPolygon2D: polygonDocument(square(0, 0, 1, 1)),
	}
	for kind := range containment {
		doc, ok := samples[kind]
		if !ok {
			t.Errorf("geometry kind %q is registered but has no sample document here; add one and "+
				"confirm the kind reports its own vertex count", kind)
			continue
		}
		c, err := CompileGeometry(doc)
		if err != nil {
			t.Errorf("kind %q: CompileGeometry: %v", kind, err)
			continue
		}
		if _, ok := c.geom.(compiledDetail); !ok {
			t.Errorf("geometry kind %q does not implement compiledDetail, so it cannot be counted "+
				"or pre-built and would be cached at the uncounted floor of %d", kind, uncountedGeometryCost)
		}
	}
}

// TestAnUncountedGeometryIsNotFree: the floor exists because 0 would make unboundedly many
// uncountable entries fit inside any bound — the one way a bound stops being a bound. halfPlane
// implements only the mandatory half of the contract, which is exactly the case being priced.
func TestAnUncountedGeometryIsNotFree(t *testing.T) {
	registerFakeKind(t)
	env, err := json.Marshal(map[string]any{"kind": fakeKind, "geometry": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := CompileGeometry(env)
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	if c.Vertices() <= 0 {
		t.Errorf("an uncounted geometry costs %d; anything that is not positive makes the cache bound vacuous", c.Vertices())
	}
}

// TestPrebuildReachesTheGeometry: Prebuild's whole value is that the index build happens on the
// inserter's goroutine before the value is shared, so what has to be observed is that the call
// arrives at the shape at all. A Prebuild that quietly did nothing would leave the lazy build in
// the hot path with every symptom — a rare stall on the single-writer loop, or a race after a
// dependency bump — appearing far from here.
func TestPrebuildReachesTheGeometry(t *testing.T) {
	rec := registerCountedKind(t)
	c, err := CompileGeometry(countedDocument("x", 12))
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	if rec.prebuilds != 0 {
		t.Fatalf("compilation pre-built %d times on its own; the test cannot tell who called", rec.prebuilds)
	}
	c.Prebuild()
	if rec.prebuilds != 1 {
		t.Errorf("Prebuild() reached the geometry %d times, want 1", rec.prebuilds)
	}
}

// TestPrebuildIsSafeAndAnswerPreserving: it runs throwaway queries against every loop, including a
// polygon big enough for s2 to actually build an index (above its 32-vertex brute-force threshold)
// and one with a hole. Nothing about the answers may change — a pre-build that perturbed a fence's
// verdict would be worse than the race it removes.
func TestPrebuildIsSafeAndAnswerPreserving(t *testing.T) {
	cases := []struct {
		name  string
		rings [][][2]float64
	}{
		{"small square", [][][2]float64{square(0, 0, 1, 1)}},
		{"square with a hole", [][][2]float64{square(0, 0, 10, 10), square(4, 4, 6, 6)}},
		{"64-gon, above the brute-force threshold", [][][2]float64{circleRing(64)}},
		{"64-gon with a hole", [][][2]float64{circleRing(64), square(-0.001, -0.001, 0.001, 0.001)}},
	}
	probes := []Position{at(0, 0), at(5, 5), at(0.5, 0.5), at(45, 45), at(-0.0005, 0.0005)}
	for _, tc := range cases {
		c, err := CompileGeometry(polygonDocument(tc.rings...))
		if err != nil {
			t.Fatalf("%s: CompileGeometry: %v", tc.name, err)
		}
		before := make([]bool, 0, len(probes))
		for _, p := range probes {
			in, err := c.geom.contains(p)
			if err != nil {
				t.Fatalf("%s: contains: %v", tc.name, err)
			}
			before = append(before, in)
		}
		c.Prebuild()
		for i, p := range probes {
			in, err := c.geom.contains(p)
			if err != nil {
				t.Fatalf("%s: contains after Prebuild: %v", tc.name, err)
			}
			if in != before[i] {
				t.Errorf("%s: probe %d answered %v before Prebuild and %v after", tc.name, i, before[i], in)
			}
		}
	}
}

// Index-build size bands, in bytes allocated. They are BANDS rather than floors on purpose: a
// one-sided bound reads as safe and is not, since "the build got much bigger" is as much a change
// worth seeing as "it stopped happening". The measured figures on the pinned golang/geo are 432,728
// bytes for a 511-vertex loop's index, 62,824 for a 64-vertex one, and 192 for a repeat call that
// builds nothing — so every bound below sits at least 8x away from the value it separates.
const (
	minIndexBuildBytes = 100 << 10
	maxIndexBuildBytes = 8 << 20
	maxIdempotentBytes = 8 << 10
	minHoleIndexBytes  = 8 << 10
	maxHoleIndexBytes  = 2 << 20
)

// allocsDuring is how many bytes f allocated. s2's index build is a large, deterministic
// allocation, which makes this the one signal that separates "the index was built" from "it was
// not" — the library exposes no freshness flag and gives identical answers either way, so the only
// alternative is timing.
func allocsDuring(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestPrebuildForcesTheIndexOnEveryRing is the direct evidence for the pre-build requirement: the
// throwaway query has to do REAL WORK, and it has to do it for the holes as well as the exterior.
//
// 🔴 THE PROBE IS EASY TO GET SUBTLY WRONG AND IMPOSSIBLE TO CATCH BY ANSWERS. s2.Loop.ContainsPoint
// short-circuits on the loop's bounding rectangle before touching the index, so a probe point
// outside the bound builds nothing and returns instantly — a "pre-built" geometry that is not, with
// every containment answer still correct. So is a probe that never runs at all. What separates them
// is the allocation the build makes.
//
// The third measurement is the hole coverage. `contains` short-circuits out of the hole scan for
// any point outside the exterior, so one whole-geometry query would leave every hole unbuilt; here
// the exterior is built by hand first, which makes anything the pre-build then allocates the holes'
// index and nothing else.
func TestPrebuildForcesTheIndexOnEveryRing(t *testing.T) {
	// 511 vertices is well past s2's 32-vertex brute-force threshold, which is the only regime in
	// which a shape index is built at all.
	big := mustCompile(t, polygonDocument(circleRing(511)))
	first := allocsDuring(func() { big.Prebuild() })
	if first < minIndexBuildBytes || first > maxIndexBuildBytes {
		t.Errorf("the first Prebuild allocated %d bytes, want between %d and %d — outside that band "+
			"it either built no index or built something quite different", first, minIndexBuildBytes, maxIndexBuildBytes)
	}
	if second := allocsDuring(func() { big.Prebuild() }); second > maxIdempotentBytes {
		t.Errorf("a repeat Prebuild allocated %d bytes (limit %d); it is supposed to be idempotent", second, maxIdempotentBytes)
	}

	holed := mustCompile(t, polygonDocument(circleRing(511), circleRingRadius(64, 0.005)))
	p, ok := holed.geom.(*polygon2D)
	if !ok {
		t.Fatalf("a POLYGON_2D document compiled to %T", holed.geom)
	}
	if len(p.holes) != 1 {
		t.Fatalf("the fixture compiled to %d holes, want 1", len(p.holes))
	}
	p.exterior.ContainsPoint(p.exterior.Vertex(0)) // build the exterior's index by hand
	holeBytes := allocsDuring(func() { holed.Prebuild() })
	if holeBytes < minHoleIndexBytes || holeBytes > maxHoleIndexBytes {
		t.Errorf("with the exterior already built, Prebuild allocated %d bytes, want between %d and %d — "+
			"below that band the hole's index was never forced", holeBytes, minHoleIndexBytes, maxHoleIndexBytes)
	}
}

// TestThePrebuildWalkReachesEveryRing pins the enumeration both prebuildIndex and vertexCount walk.
// It is the cheap structural companion to the allocation measurement above: that test proves the
// work happened, this one proves the list it happens over is the whole polygon, and a forgotten
// hole fails both rather than neither.
func TestThePrebuildWalkReachesEveryRing(t *testing.T) {
	c := mustCompile(t, polygonDocument(square(0, 0, 10, 10), square(1, 1, 2, 2), square(5, 5, 6, 6)))
	p, ok := c.geom.(*polygon2D)
	if !ok {
		t.Fatalf("a POLYGON_2D document compiled to %T", c.geom)
	}
	walked := p.loops()
	if len(walked) != 3 {
		t.Fatalf("the walk covers %d rings, want 3 (one exterior and two holes)", len(walked))
	}
	if walked[0] != p.exterior {
		t.Error("the exterior ring is not first in the walk")
	}
	for i, hole := range p.holes {
		found := false
		for _, loop := range walked {
			if loop == hole {
				found = true
			}
		}
		if !found {
			t.Errorf("hole %d is not in the walk, so nothing pre-builds or counts it", i)
		}
	}
}

// circleRing is a closed ring of n positions on a small circle centred on (0,0). n above 32 puts
// the compiled loop past s2's brute-force threshold, which is the only regime in which a shape
// index is built at all.
func circleRing(n int) [][2]float64 { return circleRingRadius(n, 0.01) }

// circleRingRadius is circleRing at a chosen radius, so a smaller ring can sit strictly inside a
// larger one as a hole.
func circleRingRadius(n int, radius float64) [][2]float64 {
	ring := make([][2]float64, 0, n+1)
	for i := 0; i < n; i++ {
		theta := 2 * math.Pi * float64(i) / float64(n)
		ring = append(ring, [2]float64{radius * math.Cos(theta), radius * math.Sin(theta)})
	}
	return append(ring, ring[0])
}

// TestUnresolvedFenceReadsAsAnErrorNeverAsAbsentOrOutside is the load-bearing property of the
// error-fence constructor.
//
// The three assertions are three different lies it has to avoid. `false, nil` is "the device is
// not in that region", which cancels a Duration hold and reads as a healthy never-firing rule.
// ErrUnknownFence is "that fence did not exist at this version", which is a claim about authoring
// history and sends the author to edit a rule that is correct. Only a plain error says what
// happened. The final case is the control: a token the set really does not hold must still answer
// ErrUnknownFence, so this test cannot be satisfied by making every miss error.
func TestUnresolvedFenceReadsAsAnErrorNeverAsAbsentOrOutside(t *testing.T) {
	unavailable := errors.New("the geometry body could not be fetched")
	fs := &FenceSet{version: 9, byToken: map[string]*Fence{
		"gate": NewErrorFence("gate", unavailable),
		"yard": NewCompiledFence("yard", mustCompile(t, polygonDocument(square(0, 0, 1, 1)))),
	}}

	in, err := fs.Contains("gate", at(0.5, 0.5))
	if err == nil {
		t.Fatalf("an unresolved fence answered (%v, nil); an unresolvable body must never read as an answer", in)
	}
	if in {
		t.Error("an unresolved fence answered inside as well as erroring")
	}
	if errors.Is(err, ErrUnknownFence) {
		t.Error("an unresolved fence reported ErrUnknownFence; it exists, its body does not")
	}
	if !errors.Is(err, unavailable) {
		t.Errorf("the reason was lost on the way out: %v", err)
	}
	if fs.Fence("gate") == nil {
		t.Error("an unresolved fence is not in the set at all, so it is indistinguishable from one never authored")
	}
	if fs.Len() != 2 {
		t.Errorf("fence set holds %d fences, want 2 (the unresolved one is retained, not dropped)", fs.Len())
	}
	if _, err := fs.Contains("never-authored", at(0.5, 0.5)); !errors.Is(err, ErrUnknownFence) {
		t.Errorf("a genuinely absent token answered %v, want ErrUnknownFence", err)
	}
	if !mustContain(t, fs, "yard", at(0.5, 0.5)) {
		t.Error("a sibling fence stopped working because another fence in the set was unresolved")
	}
}

// TestErrorFenceWithNoReasonStillErrors: the nil-error case is the one shape Contains cannot
// survive — a fence with neither geometry nor error dereferences nil — and it is exactly what a
// caller writes when it forgets to thread its error through. The constructor substitutes rather
// than honours, so the fence is at worst uninformative instead of fatal.
func TestErrorFenceWithNoReasonStillErrors(t *testing.T) {
	fs := &FenceSet{version: 1, byToken: map[string]*Fence{"gate": NewErrorFence("gate", nil)}}
	in, err := fs.Contains("gate", at(0, 0))
	if err == nil {
		t.Fatalf("NewErrorFence(token, nil) answered (%v, nil)", in)
	}
	if !strings.Contains(err.Error(), "gate") {
		t.Errorf("the substituted reason does not name the fence: %v", err)
	}
}

// TestCompiledFenceFromAFailedCompileIsAnErrorFence: CompileGeometry returns a Compiled carrying
// the KIND alongside its error, and a caller that reads a non-empty kind as success would hand
// that value straight to NewCompiledFence. The refusal is structural so that mistake produces a
// loud fence rather than a nil dereference on the first location event.
func TestCompiledFenceFromAFailedCompileIsAnErrorFence(t *testing.T) {
	broken := []byte(`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[0,0]]]}}`)
	c, err := CompileGeometry(broken)
	if err == nil {
		t.Fatal("a degenerate ring compiled without error")
	}
	if c.Kind() != KindPolygon2D {
		t.Errorf("the kind was dropped on failure: %q", c.Kind())
	}
	if c.Vertices() != 0 {
		t.Errorf("a failed compile reports %d vertices; a value with no geometry must cost nothing", c.Vertices())
	}

	fs := &FenceSet{version: 1, byToken: map[string]*Fence{
		"gate":    NewCompiledFence("gate", c),
		"nothing": NewCompiledFence("nothing", Compiled{}),
	}}
	for _, token := range []string{"gate", "nothing"} {
		in, err := fs.Contains(token, at(0, 0))
		if err == nil {
			t.Errorf("%s: a fence built from an uncompiled geometry answered (%v, nil)", token, in)
		}
	}
}

// TestCompileGeometryRefusesWhatItCannotEvaluate covers the three refusals a fetched document can
// hit, each of which must be an error rather than a fence that answers false. The reserved kinds
// are here for the same reason they are in the snapshot path: a kind nothing can evaluate must
// never produce the one outcome indistinguishable from a correct negative.
func TestCompileGeometryRefusesWhatItCannotEvaluate(t *testing.T) {
	reserved, err := json.Marshal(map[string]any{"kind": KindVoxel3D, "geometry": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		document []byte
	}{
		{"empty document", nil},
		{"unreadable envelope", []byte(`{"kind":`)},
		{"a reserved kind", reserved},
		{"a self-intersecting ring", polygonDocument([][2]float64{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}})},
	} {
		c, err := CompileGeometry(tc.document)
		if err == nil {
			t.Errorf("%s: compiled without error", tc.name)
		}
		if c.Vertices() != 0 {
			t.Errorf("%s: a refused document reports %d vertices", tc.name, c.Vertices())
		}
	}
}

// mustCompile compiles a document or fails the test.
func mustCompile(t *testing.T, document []byte) Compiled {
	t.Helper()
	c, err := CompileGeometry(document)
	if err != nil {
		t.Fatalf("CompileGeometry: %v", err)
	}
	return c
}
