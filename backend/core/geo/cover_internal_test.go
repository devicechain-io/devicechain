// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"testing"

	"github.com/golang/geo/s2"
)

// TestNeighbourPaddingCoversTheBoundaryBand drives coverRegion with a region whose boundary IS the
// covering cell's own edge, which is the only geometry where the padding decides the answer: a
// polygon edge that coincides with a cell edge leaves a point a few millimetres outside the loop
// (inside the boundary band, so Contains accepts it) in the NEIGHBOUR cell. The unpadded covering is
// asserted to miss the point (the control, proving the fixture can see the defect), the padded one
// to contain it.
func TestNeighbourPaddingCoversTheBoundaryBand(t *testing.T) {
	for _, level := range []int{6, 10, 14} {
		for _, at := range []Position{{Lat: 10, Lon: 20}, {Lat: 47, Lon: 8}, {Lat: -33, Lon: 151}, {Lat: 80, Lon: 10}, {Lat: 0, Lon: 180}} {
			cell := s2.CellFromCellID(s2.CellIDFromLatLng(s2.LatLngFromDegrees(at.Lat, at.Lon)).Parent(level))
			bare, err := coverRegion(cell, MaxCoverRanges, false)
			if err != nil {
				t.Fatal(err)
			}
			padded, err := coverRegion(cell, MaxCoverRanges, true)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				mid := s2.Point{Vector: cell.Vertex(i).Add(cell.Vertex((i + 1) % 4).Vector).Normalize()}
				out := mid.Sub(cell.Center().Vector).Normalize()
				p := s2.Point{Vector: mid.Add(out.Mul(4e-3 / EarthRadiusM)).Normalize()}
				if cell.ContainsPoint(p) {
					t.Fatalf("level %d edge %d: the probe is inside the cell", level, i)
				}
				leaf := EncodeCell(s2.CellIDFromLatLng(s2.LatLngFromPoint(p)))
				if InRanges(bare, leaf) {
					t.Fatalf("level %d edge %d: the unpadded covering already contains the probe; the fixture does not exercise the padding", level, i)
				}
				if !InRanges(padded, leaf) {
					t.Fatalf("level %d edge %d: the padded covering misses a point 4 mm outside the edge", level, i)
				}
			}
		}
	}
}
