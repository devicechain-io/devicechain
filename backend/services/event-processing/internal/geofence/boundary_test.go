// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"fmt"
	"math"
	"testing"
)

// 🔴 EVERY TEST IN THIS FILE USES A RING OF AT LEAST indexedBoundaryMinVertices POSITIONS, AND
// THAT IS THE ENTIRE REASON THE FILE EXISTS. The boundary tests next door are drawn with 4-position
// squares, which sit below the threshold and therefore exercise scanLoopBoundary and nothing else.
// When the indexed path was added, the whole package stayed green with the query replaced by a bare
// `return false` — the coverage was zero and the suite could not say so.

// tolerantOffsetDegrees is a radial offset comfortably INSIDE BoundaryToleranceRadians: 1e-9
// radians is ~6.4mm on Earth, and 2e-8 degrees is ~2.2mm. A point pushed this far off a ring is
// outside the loop's interior but still on its boundary by the convention this package promises.
const tolerantOffsetDegrees = 2e-8

// clearOffsetDegrees is a radial offset comfortably OUTSIDE the tolerance — ~11cm — so a point
// pushed this far off a ring is genuinely outside the fence. It is the negative control for every
// assertion below: without it, a boundary test that answered "inside" for everything near the ring
// would pass every one of them.
const clearOffsetDegrees = 1e-6

// ringPointAt returns the position on the ray through ring vertex i, at the ring's radius plus
// offset. For a convex ring the vertex is the farthest point of the polygon along that ray, so a
// positive offset is reliably outside the interior and a large one is reliably off the boundary.
func ringPointAt(n, i int, radius, offset float64) Position {
	theta := 2 * math.Pi * float64(i) / float64(n)
	r := radius + offset
	return Position{Lon: r * math.Cos(theta), Lat: r * math.Sin(theta)}
}

// TestAnIndexedRingKeepsItsBoundaryInclusive is the direct kill for an indexed path that answers
// "not on the boundary" for everything — the mutant the rest of the suite could not see.
//
// The two offsets are the point of the test. The near one must be INSIDE (it is within the
// tolerance the boundary convention promises) and the far one must be OUTSIDE; an implementation
// that always said either thing fails one of them.
func TestAnIndexedRingKeepsItsBoundaryInclusive(t *testing.T) {
	const n, radius = 64, 0.01
	if n < indexedBoundaryMinVertices {
		t.Fatalf("the fixture has %d positions, below the %d-position index threshold — this test "+
			"would exercise the scan and prove nothing about the query", n, indexedBoundaryMinVertices)
	}
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("circle", circleRingRadius(n, radius))})

	for i := 0; i < n; i += 7 {
		if !mustContain(t, fs, "circle", ringPointAt(n, i, radius, tolerantOffsetDegrees)) {
			t.Errorf("a point ~2.2mm outside vertex %d is not inside; the boundary is supposed to "+
				"be inclusive within ~6.4mm", i)
		}
		if mustContain(t, fs, "circle", ringPointAt(n, i, radius, clearOffsetDegrees)) {
			t.Errorf("a point ~11cm outside vertex %d is inside; the boundary tolerance is ~6.4mm", i)
		}
	}
}

// TestTheIndexedAndScannedBoundaryTestsAgree is the strongest statement available about the
// indexed path: it answers what the scan answers, over probes drawn from every regime the two
// filters split the plane into.
//
// It compares against scanLoopBoundary DIRECTLY rather than against a second fence, so the
// bounding-rectangle reject is inside the compared function and the scan is not. That is
// deliberate — the reject is one of the two things under test, and routing both sides through it
// would make a wrong reject invisible.
func TestTheIndexedAndScannedBoundaryTestsAgree(t *testing.T) {
	const n, radius = 128, 0.01
	compiled := mustCompile(t, polygonDocument(circleRingRadius(n, radius)))
	p, ok := compiled.geom.(*polygon2D)
	if !ok {
		t.Fatalf("a POLYGON_2D document compiled to %T", compiled.geom)
	}
	if p.exterior.edges == nil {
		t.Fatalf("a %d-position ring compiled without an edge index; this test would compare the "+
			"scan against itself", n)
	}

	probes := []struct {
		name string
		pos  Position
	}{
		{"centre", Position{Lon: 0, Lat: 0}},
		{"far away", Position{Lon: 10, Lat: 10}},
		{"outside the bound", Position{Lon: radius * 4, Lat: 0}},
		// Inside the bounding rectangle, outside the ring: the corner region the rectangle
		// reject cannot answer. This is the regime the index exists for.
		{"in bound, off ring", Position{Lon: radius * 0.99, Lat: radius * 0.99}},
	}
	for i := 0; i < n; i += 5 {
		probes = append(probes,
			struct {
				name string
				pos  Position
			}{fmt.Sprintf("vertex %d + 2.2mm", i), ringPointAt(n, i, radius, tolerantOffsetDegrees)},
			struct {
				name string
				pos  Position
			}{fmt.Sprintf("vertex %d + 11cm", i), ringPointAt(n, i, radius, clearOffsetDegrees)},
			struct {
				name string
				pos  Position
			}{fmt.Sprintf("vertex %d - 2.2mm", i), ringPointAt(n, i, radius, -tolerantOffsetDegrees)},
		)
	}

	agreed := 0
	for _, probe := range probes {
		pt := pointOf(probe.pos.Lat, probe.pos.Lon)
		indexed := onLoopBoundary(p.exterior, pt)
		scanned := scanLoopBoundary(p.exterior.loop, pt)
		if indexed != scanned {
			t.Errorf("%s: the indexed boundary test says %v, the scan says %v", probe.name, indexed, scanned)
			continue
		}
		if indexed {
			agreed++
		}
	}
	// 🔴 AGREEING ON "NO" EVERYWHERE IS NOT AGREEMENT. Both implementations returning false for
	// every probe would pass the loop above, so the run is only meaningful if some probe actually
	// landed on the boundary.
	if agreed == 0 {
		t.Error("no probe was on the boundary, so the two implementations agreed only on rejections")
	}
}

// TestAnIndexedHoleStillExcludesItsInterior is the kill for IncludeInteriors(true).
//
// A loop in a ShapeIndex is a two-dimensional shape, so an interior-including query answers zero
// distance for every point inside it — which would make onLoopBoundary true throughout a hole,
// invert the exclusion in `contains`, and turn a donut into a solid disc. The sibling test next
// door proves the same property with 4-position squares, i.e. entirely on the scan path.
func TestAnIndexedHoleStillExcludesItsInterior(t *testing.T) {
	const n = 64
	fs := NewFenceSet(1, []SnapshotFence{polygonFence("donut",
		circleRingRadius(n, 0.01),
		circleRingRadius(n, 0.005),
	)})

	if !mustContain(t, fs, "donut", Position{Lon: 0.0075, Lat: 0}) {
		t.Error("a point in the ring of the donut is outside it")
	}
	if mustContain(t, fs, "donut", Position{Lon: 0, Lat: 0}) {
		t.Error("a point at the centre of the hole is inside the fence; the hole's INTERIOR was " +
			"read as its boundary")
	}
	if !mustContain(t, fs, "donut", ringPointAt(n, 0, 0.005, tolerantOffsetDegrees)) {
		t.Error("a point on the hole's ring is not inside; the fence's boundary includes its holes' rings")
	}
}

// TestTheEdgeIndexIsBuiltOnlyAboveTheThreshold pins what indexedBoundaryMinVertices actually does.
// Without it the constant could drift to any value — or to zero, indexing every ring — and every
// other test in the package would still pass, because both paths answer identically by design.
//
// The threshold is INCLUSIVE, and the count it is compared against is the COMPILED vertex count —
// one fewer than the authored position count, since loopFromRing drops each ring's repeated closing
// position. circleRing's argument is the number of DISTINCT positions and it appends the closing
// one itself, so circleRing(k) compiles to exactly k vertices. The two off-by-ones cancel, which is
// worth stating because assuming they did not is what this test first caught.
func TestTheEdgeIndexIsBuiltOnlyAboveTheThreshold(t *testing.T) {
	for _, tc := range []struct {
		vertices  int
		wantIndex bool
	}{
		{indexedBoundaryMinVertices - 1, false},
		{indexedBoundaryMinVertices, true},
		{4, false},
	} {
		compiled := mustCompile(t, polygonDocument(circleRing(tc.vertices)))
		p, ok := compiled.geom.(*polygon2D)
		if !ok {
			t.Fatalf("a POLYGON_2D document compiled to %T", compiled.geom)
		}
		if n := p.exterior.loop.NumVertices(); n != tc.vertices {
			t.Fatalf("the fixture compiled to %d vertices, want %d — the case is not testing the "+
				"size it names", n, tc.vertices)
		}
		if got := p.exterior.edges != nil; got != tc.wantIndex {
			t.Errorf("a %d-vertex ring compiled with an edge index = %v, want %v (threshold %d)",
				tc.vertices, got, tc.wantIndex, indexedBoundaryMinVertices)
		}
	}
}
