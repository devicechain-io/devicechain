// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"fmt"
	"testing"

	"github.com/golang/geo/s2"
)

// benchProbes are the three regimes a containment answer falls into, and they are timed separately
// because they do not cost remotely the same thing.
//
// 🔴 outside-in-bound IS THE NEGATIVE CONTROL FOR THE BOUNDING-RECTANGLE REJECT, and without it
// these numbers would tell a flattering lie. A circle's bounding rectangle has corners the circle
// does not reach, so this probe is inside the bound and outside the ring — the one outside answer
// the reject cannot cheapen. If it ever times like `outside`, the benchmark has stopped measuring
// the boundary test and is measuring the reject.
var benchProbes = []struct {
	name string
	pos  Position
}{
	{"inside", Position{Lon: 0, Lat: 0}},
	{"outside", Position{Lon: 10, Lat: 10}},
	{"outside-in-bound", Position{Lon: 0.0099, Lat: 0.0099}},
}

// BenchmarkContains prices one inFence call end to end, at a range of ring sizes spanning the
// authoring vertex ceiling. It is what says whether the ceiling is a compute bound.
func BenchmarkContains(b *testing.B) {
	for _, n := range benchRingSizes {
		set := NewFenceSet(1, []SnapshotFence{polygonFence("f", circleRing(n))})
		for _, probe := range benchProbes {
			b.Run(fmt.Sprintf("v=%d/%s", n, probe.name), func(b *testing.B) {
				if _, err := set.Contains("f", probe.pos); err != nil {
					b.Fatalf("setup: %v", err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := set.Contains("f", probe.pos); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkBoundaryPaths times the indexed boundary query against the scan at the SAME ring sizes,
// which is the measurement indexedBoundaryMinVertices is read off. Both paths are driven directly
// rather than through a compiled fence, because a fence below the threshold carries no index and
// could not be asked the question at all.
//
// The probe is the in-bound corner deliberately: it is the only regime where the two paths do
// different amounts of work, so it is the only one whose crossover means anything.
func BenchmarkBoundaryPaths(b *testing.B) {
	pt := pointOf(0.0099, 0.0099)
	for _, n := range benchRingSizes {
		bl := mustBoundedLoop(b, n)
		forced := &boundedLoop{loop: bl.loop, edges: buildEdgeIndex(bl.loop)}

		b.Run(fmt.Sprintf("v=%d/scan", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanLoopBoundary(bl.loop, pt)
			}
		})
		b.Run(fmt.Sprintf("v=%d/indexed", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				onLoopBoundary(forced, pt)
			}
		})
	}
}

// benchRingSizes spans from a ring smaller than any real fence to eight times the authoring
// ceiling, so the shape of the curve is visible rather than two points and a line through them.
var benchRingSizes = []int{4, 16, 32, 40, 48, 64, 128, 511, 1023, 2047, 4095}

func mustBoundedLoop(b *testing.B, n int) *boundedLoop {
	b.Helper()
	compiled, err := CompileGeometry(polygonDocument(circleRing(n)))
	if err != nil {
		b.Fatalf("compiling a %d-vertex ring: %v", n, err)
	}
	p, ok := compiled.geom.(*polygon2D)
	if !ok {
		b.Fatalf("a POLYGON_2D document compiled to %T", compiled.geom)
	}
	return p.exterior
}

func buildEdgeIndex(loop *s2.Loop) *s2.ShapeIndex {
	idx := s2.NewShapeIndex()
	idx.Add(loop)
	idx.Build()
	return idx
}
