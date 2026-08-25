// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"

	"github.com/devicechain-io/dc-microservice/geo"
)

// 🔴 THE MATHS IS SPHERICAL, NOT PLANAR, AND THAT IS NOT A REFINEMENT. Treating longitude and
// latitude as x and y is wrong in two places that real fences actually sit:
//
//   - THE ANTIMERIDIAN. A fence spanning lon 179°E → 179°W is 2° wide. Planar point-in-polygon
//     reads it as a 358°-wide band covering the rest of the world, so it answers "inside" for a
//     device in the Atlantic and "outside" for the one standing in the fence.
//   - HIGH LATITUDES. The shortest path between two points at the same latitude bows POLEWARD,
//     so a "rectangle" authored as four lat/lon corners is not bounded by lines of constant
//     latitude. Measured on the box lon[-10,10] × lat[80,81]: the point (lon 0, lat 80.05) is
//     OUTSIDE the real fence — its southern edge runs at lat ~80.15 at lon 0 — while (lon -10,
//     lat 80.05) at the SAME latitude is inside. Planar maths says both are inside. The
//     disagreement is ~16 km.
//
// S2 gets both right by construction: a position becomes a unit vector, and an edge is the
// geodesic between two of them, so neither case is special-cased anywhere in this file.
//
// The library is github.com/golang/geo (pure Go, no cgo, Apache-2.0). It is PERMANENTLY
// pseudo-versioned — upstream publishes no tags — so it pins as v0.0.0-<date>-<sha>, which is a
// stable, reproducible pin, not a floating one.

// polygon2D is a compiled GeoJSON Polygon on the sphere: one exterior ring and zero or more
// interior rings (holes). Containment is exterior AND NOT strictly-inside-any-hole, with the
// boundary of EVERY ring counting as inside (see BoundaryToleranceRadians).
type polygon2D struct {
	exterior *boundedLoop
	holes    []*boundedLoop
}

// boundedLoop is one compiled ring together with the structure that makes the BOUNDARY test
// bounded rather than linear in the ring's vertex count.
//
// 🔴 WHY A SECOND INDEX EXISTS AT ALL, WHEN s2.Loop ALREADY CARRIES ONE. The loop's own index is
// unexported and reachable only through ContainsPoint, which answers INTERIOR containment. The
// boundary question — "is this point within BoundaryToleranceRadians of any edge?" — has no door
// on s2.Loop at all, so onLoopBoundary was a scan over every edge. Indexing the loop's edges
// ourselves is the only way to ask it in sub-linear time.
//
// 🔴 THE INDEX IS BUILT EAGERLY HERE, NOT LAZILY IN prebuildIndex, AND THAT IS THE POINT OF
// BUILDING OUR OWN. prebuildIndex exists to force a lazy build we do not control before a shared
// value is published; a second lazily-built index would need the same protection and would
// silently lose it on any path that compiles a geometry without publishing it through the cache.
// Built at construction there is no lazy state to protect, and a reader cannot be the builder.
type boundedLoop struct {
	loop *s2.Loop
	// edges indexes the ring's edges for the boundary test. NIL below
	// indexedBoundaryMinVertices, where the query's fixed cost exceeds the whole scan it
	// replaces — see that constant, and note that a nil here selects the scan rather than
	// skipping the test.
	edges *s2.ShapeIndex
}

// indexedBoundaryMinVertices is the ring size at and above which the boundary test goes through
// the edge index instead of scanning every edge.
//
// 🔴 IT IS A CROSSOVER, NOT A SAFETY BOUND, AND BOTH SIDES OF IT ARE CORRECT. The index query
// carries a fixed setup cost per call that the scan does not, so below this size the scan is
// simply faster; above it the scan's O(vertices) term dominates and the query's does not grow.
// Measured by BenchmarkBoundaryPaths, which runs BOTH paths at each size precisely so this number
// is read off data rather than asserted — ns/op on the in-bound probe:
//
//	vertices    32     40     48     64    128    511   4095
//	scan      1446   1881   2078   2930   5400  21627  175771
//	indexed   2808   1906   1669   2199   1727   1802    2132
//
// The two curves cross at about 40, where they are within noise of each other; 48 is the first
// size where the index wins clearly. Nothing is lost by sitting past the crossover rather than on
// it, because below it BOTH paths cost about two microseconds — the scan only becomes worth
// avoiding once it is growing, and it is the indexed column's FLATNESS, not its value at any one
// size, that is the property being bought.
//
// The consequence of the threshold is that a small ring carries no second index, which also keeps
// the geometry cache's per-vertex cost proxy honest for the ten-to-forty-vertex fences real sites
// are drawn with — those are the overwhelming majority, and they were never the ones that hurt.
const indexedBoundaryMinVertices = 48

// newBoundedLoop pairs a validated loop with its edge index, building the index only for rings
// large enough to profit from it.
func newBoundedLoop(loop *s2.Loop) *boundedLoop {
	bl := &boundedLoop{loop: loop}
	if loop.NumVertices() >= indexedBoundaryMinVertices {
		bl.edges = s2.NewShapeIndex()
		bl.edges.Add(loop)
		// Build now, so that the first concurrent reader is not the one that builds it — the
		// same obligation prebuildIndex discharges for the loop's own index, discharged here
		// where it cannot be forgotten.
		bl.edges.Build()
	}
	return bl
}

// geoJSONGeometry is the minimal RFC 7946 geometry object this package reads.
type geoJSONGeometry struct {
	Type        string        `json:"type"`
	Coordinates [][][]float64 `json:"coordinates"`
}

// compilePolygon2D turns the stored GeoJSON Polygon into loops on the sphere.
//
// Ring WINDING is normalized rather than trusted. RFC 7946 asks for a counterclockwise exterior
// ring, but it is advisory and real producers ignore it; an s2.Loop built from a clockwise ring
// describes the COMPLEMENT of the intended fence — every point on Earth except the yard. Loop
// Normalize() re-orients each ring to enclose the SMALLER of the two regions it divides the
// sphere into, which is the intended one for any fence that is not larger than a hemisphere.
// (A hemisphere-scale "fence" is not a geofence; it would be inverted, and the authoring vertex
// bound is not what stops it. Stated rather than defended.)
func compilePolygon2D(raw json.RawMessage) (geometry, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing the GeoJSON geometry object")
	}
	g := geoJSONGeometry{}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("unreadable GeoJSON geometry object: %w", err)
	}
	if g.Type != "Polygon" {
		return nil, fmt.Errorf("expected a GeoJSON Polygon, got %q", g.Type)
	}
	if len(g.Coordinates) == 0 {
		return nil, fmt.Errorf("a Polygon requires at least an exterior ring")
	}
	p := &polygon2D{}
	for i, ring := range g.Coordinates {
		loop, err := loopFromRing(ring)
		if err != nil {
			return nil, fmt.Errorf("ring %d: %w", i, err)
		}
		if i == 0 {
			p.exterior = loop
			continue
		}
		p.holes = append(p.holes, loop)
	}
	return p, nil
}

// loopFromRing builds a normalized, validated s2.Loop from one closed GeoJSON linear ring.
//
// The ring's repeated closing position is DROPPED: GeoJSON closes a ring by repeating its first
// position, while an s2.Loop is implicitly closed — leaving the duplicate in would make the loop
// self-degenerate and fail validation.
//
// Validation refuses a ring at COMPILE rather than letting it answer, so the fence carries the
// error and a rule naming it fails loudly. It is in two parts, and the second exists because the
// first is INCOMPLETE — measured, not assumed:
//
//   - s2.Loop.Validate covers non-unit, duplicate and antipodal vertices and the minimum vertex
//     count. It does NOT cover self-intersection: the Go port's crossing check is commented out
//     upstream behind a TODO (findAnyCrossing is unimplemented), so a bow-tie ring passes it. It
//     was written here first, and the test asserting a bow-tie is refused caught that it did not.
//   - selfIntersects therefore does the crossing check directly. A self-intersecting ring has no
//     well-defined interior, so without it a bow-tie would answer containment CONFIDENTLY and
//     arbitrarily — the worst available failure, since nothing about the answer looks wrong.
//
// Both are stricter than device-management's authoring validation, which checks GeoJSON structure
// and coordinate ranges only. Catching it here is second-best (the author learns at detection
// time, not at draw time) but it is the last place before an answer.
func loopFromRing(ring [][]float64) (*boundedLoop, error) {
	if len(ring) < 4 {
		return nil, fmt.Errorf("a closed ring needs at least 4 positions, got %d", len(ring))
	}
	first, last := ring[0], ring[len(ring)-1]
	if len(first) < 2 || len(last) < 2 {
		return nil, fmt.Errorf("a position needs at least [longitude, latitude]")
	}
	if first[0] != last[0] || first[1] != last[1] {
		return nil, fmt.Errorf("the ring is not closed")
	}
	pts := make([]s2.Point, 0, len(ring)-1)
	for i, pos := range ring[:len(ring)-1] {
		if len(pos) < 2 {
			return nil, fmt.Errorf("position %d needs at least [longitude, latitude]", i)
		}
		lon, lat := pos[0], pos[1]
		if math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) {
			return nil, fmt.Errorf("position %d is not a finite coordinate", i)
		}
		pts = append(pts, pointOf(lat, lon))
	}
	loop := s2.LoopFromPoints(pts)
	loop.Normalize()
	if err := loop.Validate(); err != nil {
		return nil, err
	}
	if i, j, ok := geo.LoopSelfIntersects(loop); ok {
		return nil, fmt.Errorf("the ring is self-intersecting (edges %d and %d cross)", i, j)
	}
	return newBoundedLoop(loop), nil
}

// The crossing scan and its adjacency helper moved to core/geo so the AUTHORING side
// (device-management) can refuse the same rings this evaluator refuses, instead of
// accepting a bow-tie that then fails here at compile time. The reasoning, the
// measurement behind it, and the O(V²) budget live with the code in that package.

// contains answers 2D containment: inside the exterior ring (boundary included) and not strictly
// inside any hole (a hole's own boundary counts as inside the fence, because it IS part of the
// fence's boundary and the convention is uniform).
func (p *polygon2D) contains(pos Position) (bool, error) {
	if !pos.valid() {
		return false, fmt.Errorf("the reported position is not a finite coordinate")
	}
	pt := pointOf(pos.Lat, pos.Lon)
	if !inLoop(p.exterior, pt) {
		return false, nil
	}
	for _, hole := range p.holes {
		// Strictly inside a hole ⇒ outside the fence. `inLoop` is boundary-INCLUSIVE, so the
		// exclusion has to be the strict test: on a hole's ring the point is on the fence's
		// boundary, which the convention places inside.
		if hole.loop.ContainsPoint(pt) && !onLoopBoundary(hole, pt) {
			return false, nil
		}
	}
	return true, nil
}

// loops is every compiled ring of the polygon: the exterior first, then each hole.
//
// 🔴 IT EXISTS SO THAT "TOUCH EVERY RING" IS A SINGLE ENUMERATION RATHER THAN A SHAPE REPEATED AT
// EACH CALL SITE, AND — the reason it is worth a method — so that the enumeration is TESTABLE where
// its users are not. Whether s2 actually built a loop's index is not readable from outside the
// library: there is no freshness flag, the answers are identical either way, and the only other
// signal is timing. So a prebuildIndex that quietly skipped the holes would leave no evidence
// anywhere. With the walk factored out, the claim that survives testing changes from "the index was
// built" (unobservable) to "the pre-build was handed every ring" (a list, compared by identity),
// and a forgotten hole fails a test instead of leaving a latent lazy build inside a shared value.
// vertexCount gets the same protection for free.
func (p *polygon2D) loops() []*boundedLoop {
	all := make([]*boundedLoop, 0, 1+len(p.holes))
	if p.exterior != nil {
		all = append(all, p.exterior)
	}
	for _, hole := range p.holes {
		if hole != nil {
			all = append(all, hole)
		}
	}
	return all
}

// vertexCount is the polygon's cost, summed over every ring. It counts COMPILED vertices, which is
// one fewer per ring than the authored position count — loopFromRing drops each ring's repeated
// closing position — so a fence at device-management's 512-position authoring ceiling measures 511
// here. The two numbers are deliberately not reconciled: this one is what containment iterates and
// what the compiled loops occupy, and inflating it to match an authoring bound would price a cache
// in a unit nothing in this package spends.
func (p *polygon2D) vertexCount() int {
	n := 0
	for _, bl := range p.loops() {
		n += bl.loop.NumVertices()
	}
	return n
}

// prebuildIndex forces s2's lazy shape-index build on every loop, so that a shared compiled
// polygon is fully built before any reader sees it. See Compiled.Prebuild for why that has to
// happen off the evaluation loop.
//
// 🔴 THE PROBE POINT IS THE LOOP'S OWN FIRST VERTEX, AND IT HAS TO BE. s2.Loop.ContainsPoint
// short-circuits on the loop's bounding rectangle before it ever touches the index, so a probe
// from anywhere outside the bound — the origin, say — returns false immediately and builds
// nothing, leaving a "pre-built" geometry that is not. A vertex of the loop is inside its own
// bound by construction, which is the only probe that cannot be wrong for some fence somewhere.
//
// Every ring is probed, not just the exterior: `contains` short-circuits out of the hole scan
// whenever a point falls outside the exterior, so one whole-geometry query would leave every hole's
// index unbuilt for exactly the reader that first lands inside the fence.
func (p *polygon2D) prebuildIndex() {
	for _, bl := range p.loops() {
		if bl.loop.NumVertices() > 0 {
			bl.loop.ContainsPoint(bl.loop.Vertex(0))
		}
	}
}

// inLoop is the boundary-INCLUSIVE point-in-loop test: s2's exact interior predicate, widened by
// an explicit on-the-ring test so the answer does not depend on which edge the point sits on.
// The boundary pass runs only when the interior test says no.
func inLoop(bl *boundedLoop, pt s2.Point) bool {
	if bl.loop.ContainsPoint(pt) {
		return true
	}
	return onLoopBoundary(bl, pt)
}

// onLoopBoundary reports whether the point lies on one of the loop's geodesic edges, within
// BoundaryToleranceRadians.
//
// 🔴 THIS IS THE HOT PATH OF THE WHOLE CONTAINMENT LAYER, AND IT USED TO BE THE SLOWEST THING IN
// IT. It is reached on every OUTSIDE answer — which is what a device is for nearly every fence it
// is not in — and, for a fence with holes, on every answer INSIDE a hole. Its scan is O(edges) of
// spherical point-to-segment distance, so before the two filters below, one inFence call on a
// device outside a fence at the 512-position authoring ceiling measured 23.7µs, and 174µs at 4096
// positions. Both filters exist because that cost is paid per location event, per fence a rule
// names, and the tolerance it is spent deciding is BoundaryToleranceRadians — about 6mm.
//
// 🔴 THE TWO FILTERS ARE NOT REDUNDANT AND NEITHER SUBSUMES THE OTHER, which is the thing to
// understand before deleting one of them:
//
//   - The bounding-rectangle reject answers the FAR case in constant time. A point outside the
//     ring's bound expanded by the tolerance cannot be within the tolerance of any of its edges,
//     so no scan is needed. Measured: 23.7µs → 348ns at 512 positions.
//   - It buys NOTHING in the near case. A ring's bounding rectangle has corners the ring does not
//     reach, so a point inside the bound and outside the ring — a device driving around a site,
//     the case a real fence actually sees — falls straight through it to the same full scan it
//     always was. Measured, and held there by BenchmarkBoundaryPaths' outside-in-bound case,
//     which is that filter's negative control: 24.4µs at 512 positions, unchanged.
//
// The edge index is what bounds the near case, and it is sub-linear rather than merely faster:
// 1.9µs at 512 positions and 2.2µs at 4096, against 24.4µs and 179.6µs scanned.
//
// 🔴 IncludeInteriors(false) IS LOAD-BEARING, NOT TIDINESS. A loop added to a ShapeIndex is a
// TWO-DIMENSIONAL shape, so with interiors included the query would measure distance to the
// loop's REGION and answer zero for every point inside it. This function is asked about points
// inside a hole (see contains), and every one of them would then report "on the boundary" — which
// inverts the hole exclusion and makes a donut a solid disc. TestContainsWithHole is what fails.
func onLoopBoundary(bl *boundedLoop, pt s2.Point) bool {
	tolerance := s1.Angle(BoundaryToleranceRadians)
	if bl.loop.RectBound().DistanceToLatLng(s2.LatLngFromPoint(pt)) > tolerance {
		return false
	}
	if bl.edges == nil {
		return scanLoopBoundary(bl.loop, pt)
	}
	q := s2.NewClosestEdgeQuery(bl.edges, s2.NewClosestEdgeQueryOptions().
		MaxResults(1).
		DistanceLimit(s1.ChordAngleFromAngle(tolerance)).
		IncludeInteriors(false))
	return len(q.FindEdges(s2.NewMinDistanceToPointTarget(pt))) > 0
}

// scanLoopBoundary is the unindexed boundary test: every edge, in order. It is what a ring below
// indexedBoundaryMinVertices uses, and it is kept as its own function rather than inlined so that
// BenchmarkBoundaryPaths can time it against the indexed path at the SAME ring sizes — which is
// the only way that threshold can be a measurement rather than a guess.
func scanLoopBoundary(loop *s2.Loop, pt s2.Point) bool {
	n := loop.NumVertices()
	for i := 0; i < n; i++ {
		a := loop.Vertex(i)
		b := loop.Vertex((i + 1) % n)
		if s2.DistanceFromSegment(pt, a, b) <= s1.Angle(BoundaryToleranceRadians) {
			return true
		}
	}
	return false
}

// pointOf converts WGS84 decimal degrees to a point on the unit sphere. Note the argument order
// flip: GeoJSON positions are [longitude, latitude] while s2.LatLngFromDegrees takes (lat, lng),
// which is exactly the transposition that produces a fence in the wrong hemisphere.
func pointOf(lat, lon float64) s2.Point {
	return geo.PointFromDegrees(lat, lon)
}
