// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"strings"
	"testing"

	"github.com/golang/geo/s2"
)

// Rings are [longitude, latitude], first position repeated as last.
var (
	// A plain triangle: simple, small, unambiguous.
	triangle = [][]float64{{0, 0}, {1, 0}, {0.5, 1}, {0, 0}}

	// The bow-tie. Edge (0,0)-(1,1) crosses edge (1,0)-(0,1).
	bowtie = [][]float64{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}}

	// A square that revisits its first corner in the middle — non-adjacent edges
	// SHARING a vertex, which is a pinch rather than a crossing. Two interiors
	// meeting at a point is still not a shape containment can answer for.
	pinched = [][]float64{{0, 0}, {1, 0}, {0, 0}, {1, 1}, {0, 0}}
)

func TestSimpleRingIsAccepted(t *testing.T) {
	// The counterweight to every rejection below: if this failed, each of them
	// could be passing because the checker rejects everything.
	if err := ValidateClosedRing(triangle); err != nil {
		t.Fatalf("a plain triangle was refused: %v", err)
	}
}

// 🔴 The reason this package exists. s2.Loop.Validate passes a bow-tie, so if the
// crossing check were ever "simplified" away in favour of Validate, this is the
// test that notices.
func TestBowTieIsRefusedAsSelfIntersecting(t *testing.T) {
	err := ValidateClosedRing(bowtie)
	if err == nil {
		t.Fatal("a self-intersecting ring was accepted")
	}
	if !strings.Contains(err.Error(), "self-intersecting") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// The measurement behind the paragraph in LoopSelfIntersects.
//
// 🔴 The skip below is documentation, NOT the tripwire — `go test` without -v
// prints nothing for a skip, so a skip announces an upstream change to no one.
// What actually goes red if s2 ever implements its crossing check is
// TestBowTieIsRefusedAsSelfIntersecting, which requires the substring
// "self-intersecting"; s2's own crossing message does not carry it. Do not loosen
// that assertion to a bare non-nil check, or this pair stops noticing.
func TestS2ValidateStillDoesNotDetectSelfIntersection(t *testing.T) {
	loop, err := loopFromClosedRing(bowtie)
	if err != nil {
		t.Fatalf("could not build a loop from the bow-tie: %v", err)
	}
	if err := loop.Validate(); err != nil {
		t.Skipf("s2.Loop.Validate now rejects a bow-tie (%v) — upstream implemented the "+
			"crossing check; LoopSelfIntersects may now be redundant, verify before removing", err)
	}
	if _, _, ok := LoopSelfIntersects(loop); !ok {
		t.Fatal("neither s2.Validate nor LoopSelfIntersects caught a bow-tie")
	}
}

func TestPinchedRingIsRefused(t *testing.T) {
	if err := ValidateClosedRing(pinched); err == nil {
		t.Fatal("a ring pinched at a repeated vertex was accepted")
	}
}

// 🔴 THE JUSTIFICATION FOR ValidateClosedRing EXISTING AT ALL, rather than this
// package exporting only the crossing check.
//
// The two checks overlap, but not completely, and the gap is MEASURED here rather
// than reasoned about — an earlier version of this test asserted the overlap the
// wrong way round and failed:
//
//	ring                              Validate      crossing check alone
//	dup corner, 4 loop vertices       degenerate    ALSO catches it (as a pinch)
//	dup corner, 3 loop vertices       degenerate    MISSES it
//	only two distinct vertices        degenerate    MISSES it
//
// A duplicate corner usually makes two non-adjacent edges share a point, which the
// crossing check sees as a pinch. But once the duplicate leaves only three loop
// vertices, EVERY edge pair is adjacent, the scan has nothing to compare, and only
// Validate is left. An authoring gate wired to the crossing check alone would
// accept those rings and the engine would still refuse them at compile — the same
// defect, one level down.
func TestSomeDegenerateRingsAreCaughtOnlyByLoopValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		ring [][]float64
	}{
		{"a duplicate corner leaving three loop vertices", [][]float64{{0, 0}, {1, 0}, {1, 0}, {0, 0}}},
		{"only two distinct vertices", [][]float64{{0, 0}, {1, 1}, {0, 0}, {0, 0}}},
	} {
		if _, _, ok := LoopSelfIntersects(loopOrFatal(t, tc.ring)); ok {
			t.Errorf("%s: the crossing check saw it, so it no longer demonstrates the gap "+
				"— find another ring or drop the case", tc.name)
		}
		err := ValidateClosedRing(tc.ring)
		if err == nil {
			t.Errorf("%s: accepted by the authoring gate", tc.name)
			continue
		}
		if strings.Contains(err.Error(), "self-intersecting") {
			t.Errorf("%s: attributed to the crossing check: %v", tc.name, err)
		}
	}
}

// A duplicate corner in a LARGER ring is caught by both, and reported by the one
// that runs first. Pinning the attribution keeps the error an operator reads
// pointing at what they actually did.
func TestDuplicateCornerIsReportedAsDegenerateNotAsCrossing(t *testing.T) {
	dup := [][]float64{{0, 0}, {1, 0}, {1, 0}, {0.5, 1}, {0, 0}}
	err := ValidateClosedRing(dup)
	if err == nil {
		t.Fatal("a ring with a duplicated corner was accepted")
	}
	if strings.Contains(err.Error(), "self-intersecting") {
		t.Errorf("attributed to the wrong check: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate vertex") {
		t.Errorf("did not name the duplicate: %v", err)
	}
}

// Winding must not change an answer. Both spellings of the same shape have to
// agree, or a fence would be accepted or refused depending on which direction the
// author happened to draw it.
func TestWindingDoesNotChangeTheAnswer(t *testing.T) {
	reverse := func(r [][]float64) [][]float64 {
		out := make([][]float64, len(r))
		for i := range r {
			out[i] = r[len(r)-1-i]
		}
		return out
	}
	for _, tc := range []struct {
		name string
		ring [][]float64
	}{{"triangle", triangle}, {"bowtie", bowtie}} {
		forward := ValidateClosedRing(tc.ring) == nil
		backward := ValidateClosedRing(reverse(tc.ring)) == nil
		if forward != backward {
			t.Errorf("%s: clockwise=%v counter-clockwise=%v", tc.name, forward, backward)
		}
	}
}

func TestStructurallyImpossibleRingsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		ring [][]float64
	}{
		{"too few positions", [][]float64{{0, 0}, {1, 0}, {0, 0}}},
		// Two positions cannot describe edges at all. It is here because the
		// crossing check has nothing to compare in such a ring, so a predicate
		// that answered a VERDICT for it would have to answer "does not cross" —
		// which on this question reads as "the ring is fine". Only an error can
		// say "there was nothing to ask".
		{"only two positions", [][]float64{{0, 0}, {1, 0}}},
		{"empty", [][]float64{}},
		{"a position with one element", [][]float64{{0, 0}, {1}, {0.5, 1}, {0, 0}}},
		// 🔴 Not closed. Found by the cross-service parity test, not by reading:
		// this function drops the last position to build its loop, so an unclosed
		// ring quietly became a DIFFERENT closed shape and was accepted.
		{"not closed", [][]float64{{0, 0}, {1, 0}, {0.5, 1}, {0.4, 0.9}}},
		{"first position too short to compare", [][]float64{{0}, {1, 0}, {0.5, 1}, {0}}},
		{"a NaN coordinate", [][]float64{{0, 0}, {nan(), 0}, {0.5, 1}, {0, 0}}},
		{"an infinite coordinate", [][]float64{{0, 0}, {inf(), 0}, {0.5, 1}, {0, 0}}},
	} {
		if err := ValidateClosedRing(tc.ring); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// 🔴 Out-of-range degrees are REFUSED, not wrapped, and the assertion names the
// ordinate rather than settling for non-nil.
//
// The conversion to the sphere does not reject them: latitude 91 with longitude 1
// comes back as lat 89 lon -179 — measured — so the ring below, which is otherwise
// a perfectly simple quadrilateral, used to validate clean while bounding a shape
// whose third corner had moved 180° around the planet. That is the same failure
// shape as the bow-tie this package exists for: nothing about the accepted answer
// looks wrong.
func TestOutOfRangeDegreesAreRefusedRatherThanWrapped(t *testing.T) {
	for _, tc := range []struct {
		name string
		ring [][]float64
		want string
	}{
		{"latitude above 90", [][]float64{{0, 0}, {1, 0}, {1, 91}, {0, 1}, {0, 0}},
			"position 2 latitude 91 is outside [-90, 90]"},
		{"latitude below -90", [][]float64{{0, 0}, {1, 0}, {1, -90.5}, {0, 1}, {0, 0}},
			"position 2 latitude -90.5 is outside [-90, 90]"},
		{"longitude above 180", [][]float64{{0, 0}, {1, 0}, {181, 1}, {0, 1}, {0, 0}},
			"position 2 longitude 181 is outside [-180, 180]"},
		{"longitude below -180", [][]float64{{0, 0}, {1, 0}, {-180.5, 1}, {0, 1}, {0, 0}},
			"position 2 longitude -180.5 is outside [-180, 180]"},
	} {
		err := ValidateClosedRing(tc.ring)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if err.Error() != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, err.Error(), tc.want)
		}
	}
}

// 🔴 The counterweight, and the bound polarity is the whole point of it. ±180
// longitude and ±90 latitude are LEGAL coordinates — the antimeridian and the poles
// are places — so the comparisons have to be strict. Written `<=` / `>=` instead,
// every assertion in the test above still passes while real fences at the edge of
// the coordinate system become unauthorable.
//
// Each shape below carries a boundary value on a ring the EXPORTED gate accepts,
// which is what makes this a claim about the contract rather than about the private
// loop builder: measured, ValidateClosedRing returns nil for all four.
func TestRangeBoundariesAreAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		ring [][]float64
	}{
		{"a quad against the eastern antimeridian", [][]float64{{180, 0}, {179, 0}, {179, 1}, {180, 1}, {180, 0}}},
		{"a quad against the western antimeridian", [][]float64{{-180, 0}, {-179, 0}, {-179, -1}, {-180, -1}, {-180, 0}}},
		{"a triangle with its apex on the north pole", [][]float64{{0, 89}, {1, 89}, {0.5, 90}, {0, 89}}},
		{"a triangle with its apex on the south pole", [][]float64{{0, -89}, {1, -89}, {0.5, -90}, {0, -89}}},
	} {
		if err := ValidateClosedRing(tc.ring); err != nil {
			t.Errorf("%s: a boundary coordinate was refused: %v", tc.name, err)
		}
	}
}

// Adjacency is positional and wraps. The (last, first) pair is adjacent on a
// closed loop and is exactly the pair a naive `j == i+1` misses.
func TestAdjacentEdgesWrapsAround(t *testing.T) {
	const n = 5
	if !adjacentEdges(0, n-1, n) {
		t.Error("the wrap-around pair was not treated as adjacent")
	}
	if !adjacentEdges(2, 3, n) {
		t.Error("consecutive edges were not treated as adjacent")
	}
	if adjacentEdges(1, 3, n) {
		t.Error("edges one apart were treated as adjacent")
	}
	// A triangle is all-adjacent — every pair of its 3 edges shares a vertex — so
	// a correct scan can never report a crossing in one.
	if _, _, ok := LoopSelfIntersects(loopOrFatal(t, triangle)); ok {
		t.Error("reported a crossing between adjacent edges of a triangle")
	}
}

// loopOrFatal builds the loop a crossing-check assertion needs, and fails the test
// rather than returning nil if it cannot — a nil loop would make every crossing
// assertion below it pass for the wrong reason.
func loopOrFatal(t *testing.T, ring [][]float64) *s2.Loop {
	t.Helper()
	loop, err := loopFromClosedRing(ring)
	if err != nil {
		t.Fatalf("could not build a loop from %v: %v", ring, err)
	}
	return loop
}

func TestPointFromDegreesIsLatitudeFirst(t *testing.T) {
	// 🔴 The argument order is (lat, lon) while GeoJSON positions are [lon, lat].
	// Asserting it with a deliberately asymmetric point is the only thing standing
	// between that mismatch and a fence on the wrong continent.
	p := PointFromDegrees(41.8902, 12.4924)
	ll := s2.LatLngFromPoint(p)
	if got := ll.Lat.Degrees(); got < 41.89 || got > 41.891 {
		t.Errorf("latitude round-tripped as %v", got)
	}
	if got := ll.Lng.Degrees(); got < 12.492 || got > 12.493 {
		t.Errorf("longitude round-tripped as %v", got)
	}
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }
