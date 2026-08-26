// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package geo holds the ring predicates that the AUTHORING side and the
// EVALUATION side of geofencing have to agree on.
//
// It exists because they did not. device-management checked a fence's GeoJSON
// structure and coordinate ranges and accepted the result; the detection engine
// additionally required the ring to bound a well-defined area, and enforced that
// at COMPILE time. So a ring that is structurally perfect but geometrically
// impossible — a bow-tie, a ring doubling back on a repeated vertex — saved
// cleanly, sat in the registry looking healthy, and failed only later when a rule
// named it. The author learned at detection time about a mistake made at draw
// time, and the fence in between answered nothing while looking fine.
//
// The checks lived under event-processing's internal/ tree, which no other module
// may import. Moving them here is what lets authoring refuse exactly what
// evaluation refuses — the same rings, for the same reasons, with the same answer.
//
// These predicates re-check the structure they depend on (length, closure, finite
// coordinates) rather than trusting a caller to have done it. Both callers DO
// check first, and both produce better messages, so in practice this package's
// structural errors are unreachable from either — but a precondition enforced only
// by a comment is not enforced, and the parity test caught exactly that: an
// unclosed ring was silently reinterpreted as a different, closed shape.
package geo

import (
	"fmt"
	"math"

	"github.com/golang/geo/s2"
)

// PointFromDegrees converts WGS84 degrees to a point on the unit sphere.
//
// Exported so both callers build the same point from the same degrees. Two
// spellings of this conversion is precisely how two services begin to disagree
// about where a boundary is, and the disagreement would be invisible: each side
// would be self-consistent.
func PointFromDegrees(lat, lon float64) s2.Point {
	return s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lon))
}

// ValidateClosedRing reports why a closed GeoJSON ring cannot bound an area, or
// nil if it can. Positions are [longitude, latitude] and the first is expected to
// be repeated as the last.
//
// This is the AUTHORING gate, and it deliberately covers everything the evaluator
// refuses rather than only the headline case:
//
//   - what s2.Loop.Validate covers — non-unit vertices, fewer than three
//     vertices, and ADJACENT duplicate vertices (degenerate edges). It also
//     carries an adjacent-antipodal check, but that compares points for exact
//     equality and a point built from degrees does not land exactly on the
//     antipode — LatLngFromDegrees(0, 180) leaves y ≈ 1.2e-16, measured — so it
//     is not expected to fire for degree-derived points. That is a claim about
//     the cases checked, not a proof over all inputs; a pair that did trip it
//     would produce a confusingly worded REFUSAL, which is the safe direction.
//   - and self-intersection, which Validate does NOT cover at all.
//
// Covering only the second would have moved the gap rather than closed it. The
// two overlap heavily — a duplicate corner usually makes two non-adjacent edges
// share a point, which the crossing scan sees as a pinch — but once a duplicate
// leaves only three loop vertices, every edge pair is adjacent, the scan has
// nothing to compare, and Validate is the only thing left. Those rings would have
// saved clean and died at compile with a different error.
//
// The property worth holding, and the one worth testing, is that a ring the
// authoring gate ACCEPTS always compiles in the engine.
func ValidateClosedRing(ring [][]float64) error {
	loop, err := loopFromClosedRing(ring)
	if err != nil {
		return err
	}
	if err := loop.Validate(); err != nil {
		return fmt.Errorf("the ring does not bound an area: %w", err)
	}
	if i, j, ok := LoopSelfIntersects(loop); ok {
		return fmt.Errorf("the ring is self-intersecting (edges %d and %d cross)", i, j)
	}
	return nil
}

// loopFromClosedRing drops the repeated closing position and builds a normalized
// loop. Normalizing BEFORE any check means winding cannot change an answer: the
// same ring drawn clockwise and counter-clockwise must agree about whether it
// crosses itself.
func loopFromClosedRing(ring [][]float64) (*s2.Loop, error) {
	if len(ring) < 4 {
		return nil, fmt.Errorf("a closed ring needs at least 4 positions, got %d", len(ring))
	}
	// 🔴 Closure is CHECKED, not assumed, even though both callers check it first
	// and report it better. It was assumed once — stated only in the package
	// comment — and the parity test caught it immediately: this function drops the
	// last position to build its loop, so an UNCLOSED ring silently became a
	// different, closed shape, and the authoring gate accepted a ring the engine
	// then refused. A precondition that only a comment enforces is not enforced.
	first, last := ring[0], ring[len(ring)-1]
	if len(first) < 2 || len(last) < 2 {
		return nil, fmt.Errorf("a position needs at least [longitude, latitude]")
	}
	if first[0] != last[0] || first[1] != last[1] {
		return nil, fmt.Errorf("the ring is not closed: first position %v != last %v", first, last)
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
		pts = append(pts, PointFromDegrees(lat, lon))
	}
	loop := s2.LoopFromPoints(pts)
	loop.Normalize()
	return loop, nil
}

// RingSelfIntersects reports the first pair of non-adjacent edges of a closed ring
// that cross, if any. It is the narrow entry point for callers that only want the
// crossing question; ValidateClosedRing is the one authoring should use.
//
// A ring this cannot build a loop from reports false rather than guessing — the
// caller owns those cases and diagnoses them better.
func RingSelfIntersects(ring [][]float64) (int, int, bool) {
	loop, err := loopFromClosedRing(ring)
	if err != nil {
		return 0, 0, false
	}
	return LoopSelfIntersects(loop)
}

// LoopSelfIntersects reports the first pair of NON-ADJACENT edges of the loop that
// cross, if any. Adjacent edges are skipped because they legitimately share a
// vertex — including the wrap-around pair (last, first), which is adjacent on a
// closed loop and is the pair a naive index comparison misses.
//
// 🔴 THIS IS NOT REDUNDANT WITH s2.Loop.Validate, and the temptation to delete it
// is the reason this paragraph exists. Validate does NOT detect self-intersection:
// the Go port's crossing check is commented out upstream behind a TODO
// (`findAnyCrossing` is unimplemented), so a bow-tie passes it cleanly. Measured
// against the pinned module, not assumed — and a comment claiming the opposite was
// once written in this codebase and caught by the bow-tie test that followed it.
//
// A self-intersecting ring has no well-defined interior, so without this a bow-tie
// answers containment CONFIDENTLY and arbitrarily. That is the worst available
// failure, because nothing about the answer looks wrong.
//
// O(V²), with V bounded at governance.MaxGeoFencePositionCeiling by the authoring position
// limit — the platform MAXIMUM, since the per-tenant ceiling is a tier setting an operator can
// raise up to it. At the DEFAULT ceiling of 512 that is ~131k orientation
// tests worst case, paid once per fence per fence-set version at authoring or
// compile time and never per event. That is what makes the quadratic acceptable
// rather than a hazard.
func LoopSelfIntersects(loop *s2.Loop) (int, int, bool) {
	n := loop.NumVertices()
	for i := 0; i < n; i++ {
		a, b := loop.Vertex(i), loop.Vertex(i+1)
		for j := i + 1; j < n; j++ {
			if adjacentEdges(i, j, n) {
				continue
			}
			c, d := loop.Vertex(j), loop.Vertex(j+1)
			// Cross is a proper interior crossing. MaybeCross means two edges SHARE a
			// vertex, which for NON-adjacent edges is a pinched ring — also not a
			// simple polygon, so it is refused too rather than left to answer from a
			// shape with two interiors.
			if s := s2.CrossingSign(a, b, c, d); s != s2.DoNotCross {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

// adjacentEdges reports whether edges i and j of an n-edge closed loop share a
// vertex by position.
func adjacentEdges(i, j, n int) bool {
	return j == i+1 || (i == 0 && j == n-1)
}
