// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

const (
	// MaxCoverVertices is the most ring vertices (exterior and holes, summed over the set) a
	// covering will be computed for.
	MaxCoverVertices = 1000
	// MaxCoverRanges is the most ranges a covering may return, and the most a caller may ask for.
	MaxCoverRanges = 256

	// coverMaxLevel stops the coverer subdividing below roughly six-metre cells. A finer cell buys
	// no precision a query can use, because the exact check runs on every candidate regardless.
	coverMaxLevel = 20
)

// ErrTooComplex is returned when a region cannot be covered within the vertex or range caps. The
// coverer may use coarser cells to stay inside the range budget (a coarser covering is still
// conservative, it only admits more candidates for the exact check to remove), but when even the
// coarsest covering needs more ranges than allowed, the caller is told the question is too big
// rather than being handed a covering that exceeds what the index was promised to scan.
var ErrTooComplex = errors.New("the region is too complex to cover within the vertex and range caps")

// ErrInvalidArgument is returned for a malformed query argument.
var ErrInvalidArgument = errors.New("invalid spatial argument")

// CoverPolygonSet returns inclusive, encoded, merged ranges whose union contains the encoded leaf
// cell of EVERY position for which Contains(set, position) is true. The covering is conservative:
// it may contain positions outside the set (the exact check removes those) but it never misses one
// inside. That includes the boundary band, so a point within BoundaryToleranceRadians of an edge
// is covered even though it lies outside the loop.
//
// Holes are ignored (a covering that includes a hole's cells is still conservative). Ranges are
// sorted and non-overlapping, and ranges that touch are merged. An empty set is refused rather
// than answered with an empty covering, which a caller could mistake for "nothing matches".
func CoverPolygonSet(set []*Polygon, maxRanges int) ([]Range, error) {
	if err := checkMaxRanges(maxRanges); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("%w: the polygon set is empty", ErrInvalidArgument)
	}
	vertices := 0
	for _, p := range set {
		if p == nil {
			return nil, fmt.Errorf("%w: nil polygon", ErrInvalidArgument)
		}
		vertices += p.Vertices()
	}
	if vertices > MaxCoverVertices {
		return nil, fmt.Errorf("%w: %d vertices, the cap is %d", ErrTooComplex, vertices, MaxCoverVertices)
	}
	region := loopUnion{}
	for _, p := range set {
		region.loops = append(region.loops, p.Exterior.Loop)
	}
	return coverRegion(region, maxRanges, true)
}

// CoverCap returns the conservative covering of the spherical cap of radiusM metres around center.
// The cap is widened by a few millimetres so that a position the exact distance check calls inside
// (distance <= radius) is never lost to the rounding of the cap's own chord-angle arithmetic.
func CoverCap(center Position, radiusM float64, maxRanges int) ([]Range, error) {
	if err := checkMaxRanges(maxRanges); err != nil {
		return nil, err
	}
	if err := center.Validate(); err != nil {
		return nil, err
	}
	if err := checkRadius(radiusM); err != nil {
		return nil, err
	}
	angle := s1.Angle(radiusM/EarthRadiusM) + s1.Angle(BoundaryToleranceRadians)
	return coverRegion(s2.CapFromCenterAngle(center.point(), angle), maxRanges, false)
}

func checkMaxRanges(maxRanges int) error {
	if maxRanges < 1 || maxRanges > MaxCoverRanges {
		return fmt.Errorf("%w: maxRanges %d is outside [1, %d]", ErrInvalidArgument, maxRanges, MaxCoverRanges)
	}
	return nil
}

func checkRadius(radiusM float64) error {
	if math.IsNaN(radiusM) || math.IsInf(radiusM, 0) || radiusM < 0 || radiusM > math.Pi*EarthRadiusM {
		return fmt.Errorf("%w: radius %v m is not in [0, half the Earth's circumference]", ErrInvalidArgument, radiusM)
	}
	return nil
}

// coverRegion covers the region with at most maxRanges merged ranges.
//
// padNeighbors widens every covering cell by its same-level neighbours (edge and corner). A
// polygon's boundary band extends BoundaryToleranceRadians (about 6 mm) outside its loop, so a
// point the exact check accepts can sit in a cell that does not intersect the loop. Cells are at
// least several metres across (the coverer stops at coverMaxLevel), so a point within millimetres
// of a covered cell is always in that cell or one of its neighbours, and padding by neighbours is
// both cheap and provably sufficient. The alternative, asking an edge-distance query about every
// candidate cell, is exact to the millimetre and measured at several milliseconds per covering.
//
// The coverer is given a third of the range budget when padding, since padding adds a ring.
func coverRegion(region s2.Region, maxRanges int, padNeighbors bool) ([]Range, error) {
	budget := maxRanges
	if padNeighbors {
		budget = max(1, maxRanges/3)
	}
	rc := &s2.RegionCoverer{MinLevel: 0, MaxLevel: coverMaxLevel, LevelMod: 1, MaxCells: budget}
	union := rc.Covering(region)
	if padNeighbors {
		padded := make(s2.CellUnion, 0, len(union)*5)
		for _, id := range union {
			padded = append(padded, id)
			padded = append(padded, id.AllNeighbors(id.Level())...)
		}
		padded.Normalize()
		union = padded
	}
	ranges := make([]Range, 0, len(union))
	for _, id := range union {
		ranges = append(ranges, Range{Lo: EncodeCell(id.RangeMin()), Hi: EncodeCell(id.RangeMax())})
	}
	merged := mergeRanges(ranges)
	if len(merged) > maxRanges {
		return nil, fmt.Errorf("%w: %d ranges, the cap is %d", ErrTooComplex, len(merged), maxRanges)
	}
	return merged, nil
}

// mergeRanges sorts inclusive ranges and joins those that overlap or touch. Ranges are compared in
// ENCODED space, which is only meaningful because EncodeCell preserves order.
func mergeRanges(in []Range) []Range {
	if len(in) == 0 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Lo < in[j].Lo })
	out := []Range{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		// Leaf cell ids are ODD, so two adjacent leaves differ by 2 and ranges that touch have
		// Lo == Hi+2, not Hi+1. Merging only on +1 leaves every touching pair split in two (the
		// covering stays correct but spends its range budget twice), and it hides the one pair
		// that matters most: the last leaf of face 3 and the first leaf of face 4, which meet
		// across the sign boundary. last.Hi near MaxInt64 would overflow the +2; nothing can start
		// after it anyway.
		if last.Hi >= math.MaxInt64-1 || r.Lo <= last.Hi+2 {
			if r.Hi > last.Hi {
				last.Hi = r.Hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// InRanges reports whether an encoded cell lies in any of the sorted, merged ranges.
func InRanges(ranges []Range, cell int64) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].Hi >= cell })
	return i < len(ranges) && ranges[i].Lo <= cell
}

// loopUnion is the region the coverer sees for a polygon set: the union of the exterior loops.
// Holes are ignored, which keeps the covering conservative.
type loopUnion struct{ loops []*s2.Loop }

func (u loopUnion) CapBound() s2.Cap {
	c := s2.EmptyCap()
	for _, l := range u.loops {
		c = c.AddCap(l.CapBound())
	}
	return c
}

func (u loopUnion) RectBound() s2.Rect {
	r := s2.EmptyRect()
	for _, l := range u.loops {
		r = r.Union(l.RectBound())
	}
	return r
}

func (u loopUnion) ContainsCell(c s2.Cell) bool {
	for _, l := range u.loops {
		if l.ContainsCell(c) {
			return true
		}
	}
	return false
}

func (u loopUnion) IntersectsCell(c s2.Cell) bool {
	for _, l := range u.loops {
		if l.IntersectsCell(c) {
			return true
		}
	}
	return false
}

func (u loopUnion) ContainsPoint(pt s2.Point) bool {
	for _, l := range u.loops {
		if l.ContainsPoint(pt) {
			return true
		}
	}
	return false
}

func (u loopUnion) CellUnionBound() []s2.CellID { return u.CapBound().CellUnionBound() }
