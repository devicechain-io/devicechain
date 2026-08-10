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
	exterior *s2.Loop
	holes    []*s2.Loop
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
func loopFromRing(ring [][]float64) (*s2.Loop, error) {
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
	return loop, nil
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
		if hole.ContainsPoint(pt) && !onLoopBoundary(hole, pt) {
			return false, nil
		}
	}
	return true, nil
}

// inLoop is the boundary-INCLUSIVE point-in-loop test: s2's exact interior predicate, widened by
// an explicit on-the-ring test so the answer does not depend on which edge the point sits on.
// The boundary pass runs only when the interior test says no, so the common inside/well-outside
// answers cost one O(vertices) pass, not two.
func inLoop(loop *s2.Loop, pt s2.Point) bool {
	if loop.ContainsPoint(pt) {
		return true
	}
	return onLoopBoundary(loop, pt)
}

// onLoopBoundary reports whether the point lies on one of the loop's geodesic edges, within
// BoundaryToleranceRadians.
func onLoopBoundary(loop *s2.Loop, pt s2.Point) bool {
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
