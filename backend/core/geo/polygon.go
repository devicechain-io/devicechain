// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"fmt"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// BoundaryToleranceRadians is the half-width of the band around a polygon's rings that counts as
// ON the boundary.
//
// 🔴 THE BOUNDARY IS INSIDE. That is the convention, it is held for every ring — a point on a
// hole's ring is inside the polygon too, because a hole's ring is part of the polygon's boundary —
// and it is not what S2 alone gives you. S2's own point-in-loop predicate is exact and
// deterministic but splits the boundary between the two adjacent regions, so a point sitting
// exactly on one edge of a square answers "inside" while a point on the adjacent edge answers
// "outside". A polygon whose answer depends on WHICH edge you stand on is not a contract anyone can
// hold, so containment adds an explicit boundary test.
//
// 1e-9 radians is ~6 mm on Earth's surface — four orders of magnitude below the accuracy of any
// positioning technology a device reports with, so the band is invisible in practice while keeping
// the exactly-on-the-edge case from turning on the last bits of a float64 (an on-edge point
// measures ~1e-16 rad from its edge, not 0).
//
// This is the single definition: the detection engine's geofence containment and the spatial
// engine in this package both read it, so the two cannot disagree about where an edge is.
const BoundaryToleranceRadians = 1e-9

// IndexedBoundaryMinVertices is the ring size at and above which the boundary test goes through an
// edge index instead of scanning every edge. It is a measured CROSSOVER, not a safety bound, and
// both sides of it give the same answer: below it the index's fixed per-query cost exceeds the
// whole scan it would replace.
const IndexedBoundaryMinVertices = 48

// Ring is one validated ring together with the structure that makes the boundary test bounded.
//
// Edges is nil below IndexedBoundaryMinVertices, where a nil selects the scan rather than skipping
// the test. The index is built eagerly by NewRing so that a Ring is immutable and safe to share
// between goroutines: a reader is never the builder.
type Ring struct {
	Loop  *s2.Loop
	Edges *s2.ShapeIndex
}

// NewRing pairs a validated loop with its edge index, building the index only for rings large
// enough to profit from it.
func NewRing(loop *s2.Loop) Ring {
	r := Ring{Loop: loop}
	if loop.NumVertices() >= IndexedBoundaryMinVertices {
		r.Edges = s2.NewShapeIndex()
		r.Edges.Add(loop)
		r.Edges.Build()
	}
	return r
}

// InRing is the boundary-INCLUSIVE point-in-ring test: s2's exact interior predicate, widened by an
// explicit on-the-ring test so the answer does not depend on which edge the point sits on. The
// boundary pass runs only when the interior test says no.
func InRing(r Ring, pt s2.Point) bool {
	if r.Loop.ContainsPoint(pt) {
		return true
	}
	return OnBoundary(r, pt)
}

// OnBoundary reports whether the point lies on one of the ring's geodesic edges, within
// BoundaryToleranceRadians.
//
// 🔴 IncludeInteriors(false) IS LOAD-BEARING, NOT TIDINESS. A loop added to a ShapeIndex is a
// TWO-DIMENSIONAL shape, so with interiors included the query would measure distance to the loop's
// REGION and answer zero for every point inside it. This function is asked about points inside a
// hole, and every one of them would then report "on the boundary" — which inverts the hole
// exclusion and makes a donut a solid disc.
func OnBoundary(r Ring, pt s2.Point) bool {
	tolerance := s1.Angle(BoundaryToleranceRadians)
	// A point outside the ring's bound expanded by the tolerance cannot be within the tolerance of
	// any of its edges. This answers the far case in constant time.
	if r.Loop.RectBound().DistanceToLatLng(s2.LatLngFromPoint(pt)) > tolerance {
		return false
	}
	if r.Edges == nil {
		return ScanBoundary(r.Loop, pt)
	}
	q := s2.NewClosestEdgeQuery(r.Edges, s2.NewClosestEdgeQueryOptions().
		MaxResults(1).
		DistanceLimit(s1.ChordAngleFromAngle(tolerance)).
		IncludeInteriors(false))
	return len(q.FindEdges(s2.NewMinDistanceToPointTarget(pt))) > 0
}

// ScanBoundary is the unindexed boundary test: every edge, in order. It is its own function so a
// benchmark can time it against the indexed path at the same ring sizes, which is the only way
// IndexedBoundaryMinVertices can be a measurement rather than a guess.
func ScanBoundary(loop *s2.Loop, pt s2.Point) bool {
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

// Polygon is a compiled GeoJSON Polygon on the sphere: one exterior ring and zero or more holes.
// Containment is exterior AND NOT strictly-inside-any-hole, with the boundary of EVERY ring
// counting as inside.
type Polygon struct {
	Exterior Ring
	Holes    []Ring
}

// NewPolygon compiles GeoJSON Polygon coordinates (an exterior ring, then holes; positions are
// [longitude, latitude]) through the same gate authoring uses, so a ring that authoring refuses —
// a bow-tie, a ring past a pole, an unclosed ring — is refused here with the same error. Nothing is
// repaired and nothing is skipped.
func NewPolygon(coordinates [][][]float64) (*Polygon, error) {
	if len(coordinates) == 0 {
		return nil, fmt.Errorf("a Polygon requires at least an exterior ring")
	}
	p := &Polygon{}
	for i, ring := range coordinates {
		loop, err := LoopFromClosedRing(ring)
		if err != nil {
			return nil, fmt.Errorf("ring %d: %w", i, err)
		}
		if i == 0 {
			p.Exterior = NewRing(loop)
			continue
		}
		p.Holes = append(p.Holes, NewRing(loop))
	}
	return p, nil
}

// ContainsPoint answers boundary-inclusive containment of a point on the sphere.
func (p *Polygon) ContainsPoint(pt s2.Point) bool {
	if !InRing(p.Exterior, pt) {
		return false
	}
	for _, hole := range p.Holes {
		// Strictly inside a hole means outside the polygon. InRing is boundary-INCLUSIVE, so the
		// exclusion has to be the strict test: on a hole's ring the point is on the polygon's
		// boundary, which the convention places inside.
		if hole.Loop.ContainsPoint(pt) && !OnBoundary(hole, pt) {
			return false
		}
	}
	return true
}

// Vertices is the polygon's cost, summed over every ring.
func (p *Polygon) Vertices() int {
	n := p.Exterior.Loop.NumVertices()
	for _, h := range p.Holes {
		n += h.Loop.NumVertices()
	}
	return n
}
