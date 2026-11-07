// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/geo"
	"github.com/devicechain-io/dc-microservice/geo/geotest"
	"github.com/golang/geo/s2"
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// ring builds a closed counter-clockwise GeoJSON ring from [lon, lat] corners.
func ring(corners ...[2]float64) [][]float64 {
	out := make([][]float64, 0, len(corners)+1)
	for _, c := range corners {
		out = append(out, []float64{c[0], c[1]})
	}
	return append(out, []float64{corners[0][0], corners[0][1]})
}

func square(lat, lon, half float64) [][]float64 {
	return ring([2]float64{lon - half, lat - half}, [2]float64{lon + half, lat - half},
		[2]float64{lon + half, lat + half}, [2]float64{lon - half, lat + half})
}

func mustPolygon(t testing.TB, rings ...[][]float64) *geo.Polygon {
	t.Helper()
	p, err := geo.NewPolygon(rings)
	if err != nil {
		t.Fatalf("NewPolygon: %v", err)
	}
	return p
}

func within(t testing.TB, polys ...*geo.Polygon) geo.Region {
	t.Helper()
	r, err := geo.Within(polys)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func installedAt(token string, lat, lon float64) geo.Candidate {
	return geo.Candidate{Token: token, Installed: &geo.Position{Lat: lat, Lon: lon}}
}

// jitter returns n candidates scattered around (lat, lon) by up to spread degrees.
func jitter(rng *rand.Rand, prefix string, lat, lon, spread float64, n int) []geo.Candidate {
	out := make([]geo.Candidate, 0, n)
	for i := 0; i < n; i++ {
		la := lat + (rng.Float64()*2-1)*spread
		lo := lon + (rng.Float64()*2-1)*spread
		la = math.Max(-90, math.Min(90, la))
		lo = wrapLon(lo)
		out = append(out, installedAt(fmt.Sprintf("%s-%04d", prefix, i), la, lo))
	}
	return out
}

func wrapLon(lo float64) float64 {
	for lo > 180 {
		lo -= 360
	}
	for lo < -180 {
		lo += 360
	}
	return lo
}

// agree is the oracle comparison: the indexed answer must be IDENTICAL to brute force, including
// order, and it returns the matched tokens for fixtures that also assert what matched.
func agree(t testing.TB, label string, cands []geo.Candidate, region geo.Region, mode geo.Mode) []geo.Result {
	t.Helper()
	ix, err := geotest.NewMemIndex(cands)
	if err != nil {
		t.Fatal(err)
	}
	want, err := geotest.BruteForce(cands, region, mode, t0)
	if err != nil {
		t.Fatalf("%s: brute force: %v", label, err)
	}
	got, err := geotest.Indexed(ix, region, mode, t0)
	if err != nil {
		t.Fatalf("%s: indexed: %v", label, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: indexed != brute force\n indexed: %d results\n brute:   %d results", label, len(got), len(want))
	}
	return got
}

func TestEncodeCellPreservesUnsignedOrder(t *testing.T) {
	edges := []uint64{0, 1, 1<<63 - 1, 1 << 63, 1<<63 + 1, math.MaxUint64 - 1, math.MaxUint64}
	rng := rand.New(rand.NewSource(1))
	ids := append([]uint64{}, edges...)
	for i := 0; i < 20000; i++ {
		ids = append(ids, rng.Uint64())
	}
	for i := 0; i < len(ids)-1; i++ {
		a, b := ids[i], ids[i+1]
		ea, eb := geo.EncodeCell(s2.CellID(a)), geo.EncodeCell(s2.CellID(b))
		if (a < b) != (ea < eb) || (a == b) != (ea == eb) {
			t.Fatalf("order not preserved: %#x vs %#x encode to %d vs %d", a, b, ea, eb)
		}
		if geo.DecodeCell(ea) != s2.CellID(a) {
			t.Fatalf("round trip failed for %#x", a)
		}
	}
	// The unsigned midpoint is the sign boundary and must map across zero.
	if geo.EncodeCell(s2.CellID(1<<63-1)) != -1 || geo.EncodeCell(s2.CellID(1<<63)) != 0 {
		t.Fatalf("the unsigned midpoint does not map across zero")
	}
}

// faceCenters are one position per cube face; the test asserts they really are on those faces so a
// fixture cannot quietly stop covering a face.
var faceCenters = [6][2]float64{{0, 0}, {0, 90}, {80, 10}, {0, 179}, {0, -90}, {-80, 10}}

func TestFixturesCoverEverySixCubeFaces(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for face, c := range faceCenters {
		lat, lon := c[0], c[1]
		if got := s2.CellIDFromLatLng(s2.LatLngFromDegrees(lat, lon)).Face(); got != face {
			t.Fatalf("fixture for face %d is on face %d", face, got)
		}
		poly := mustPolygon(t, square(lat, lon, 0.5))
		cands := jitter(rng, fmt.Sprintf("f%d", face), lat, lon, 1.2, 300)
		hits := agree(t, fmt.Sprintf("face %d within", face), cands, within(t, poly), geo.ModeInstalled)
		if len(hits) == 0 || len(hits) == len(cands) {
			t.Fatalf("face %d fixture is degenerate: %d of %d match", face, len(hits), len(cands))
		}
		near, _ := geo.Near(geo.Position{Lat: lat, Lon: lon}, 50_000)
		agree(t, fmt.Sprintf("face %d near", face), cands, near, geo.ModeInstalled)
	}
}

// TestUnsignedMidpoint puts a polygon on the one place where the last leaf of face 3 and the first
// leaf of face 4 meet, so that the covering contains ranges which touch ACROSS the sign boundary.
// A signed scan without the high-bit flip has Lo > Hi for such a range and drops everything in it.
func TestUnsignedMidpoint(t *testing.T) {
	lastOfFace3 := s2.LatLngFromPoint(s2.Point{Vector: s2.CellID(1<<63 - 1).Point().Vector})
	firstOfFace4 := s2.LatLngFromPoint(s2.CellID(1<<63 + 1).Point())
	lat := (lastOfFace3.Lat.Degrees() + firstOfFace4.Lat.Degrees()) / 2
	lon := (lastOfFace3.Lng.Degrees() + firstOfFace4.Lng.Degrees()) / 2
	poly := mustPolygon(t, square(lat, lon, 0.4))
	rng := rand.New(rand.NewSource(3))
	cands := jitter(rng, "mid", lat, lon, 1.0, 600)
	// A fixture only counts if it really has points on both sides of the midpoint.
	var below, above int
	for _, c := range cands {
		if uint64(s2.CellIDFromLatLng(s2.LatLngFromDegrees(c.Installed.Lat, c.Installed.Lon))) < 1<<63 {
			below++
		} else {
			above++
		}
	}
	if below < 20 || above < 20 {
		t.Fatalf("the midpoint fixture is one-sided: %d below, %d above", below, above)
	}
	ranges, err := geo.CoverPolygonSet([]*geo.Polygon{poly}, geo.MaxCoverRanges)
	if err != nil {
		t.Fatal(err)
	}
	crosses := false
	for _, r := range ranges {
		if r.Lo > r.Hi {
			t.Fatalf("inverted range %v", r)
		}
		if uint64(geo.DecodeCell(r.Lo)) > uint64(geo.DecodeCell(r.Hi)) {
			t.Fatalf("range %v is inverted in unsigned cell order", r)
		}
		if r.Lo < 0 && r.Hi >= 0 {
			crosses = true
		}
	}
	if !crosses {
		t.Fatalf("no range spans the unsigned midpoint; the fixture does not test it: %v", ranges)
	}
	hits := agree(t, "midpoint", cands, within(t, poly), geo.ModeInstalled)
	if len(hits) < 20 {
		t.Fatalf("only %d matches", len(hits))
	}
}

func TestAntimeridianAndPole(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	// A square straddling the antimeridian.
	am := mustPolygon(t, ring([2]float64{179, -1}, [2]float64{-179, -1}, [2]float64{-179, 1}, [2]float64{179, 1}))
	cands := jitter(rng, "am", 0, 180, 3, 800)
	hits := agree(t, "antimeridian", cands, within(t, am), geo.ModeInstalled)
	var east, west int
	for _, h := range hits {
		if h.Position.Lon > 0 {
			east++
		} else {
			west++
		}
	}
	if east == 0 || west == 0 {
		t.Fatalf("the antimeridian fixture matched one side only: %d east, %d west", east, west)
	}
	// A ring around the north pole, defined by points all at the same latitude.
	var pole [][2]float64
	for lon := -180.0; lon < 180; lon += 45 {
		pole = append(pole, [2]float64{lon, 85})
	}
	pp := mustPolygon(t, ring(pole...))
	cands = jitter(rng, "np", 90, 0, 10, 800)
	hits = agree(t, "north pole", cands, within(t, pp), geo.ModeInstalled)
	if len(hits) == 0 || len(hits) == len(cands) {
		t.Fatalf("pole fixture is degenerate: %d of %d", len(hits), len(cands))
	}
	if in, _ := geo.Contains([]*geo.Polygon{pp}, geo.Position{Lat: 90, Lon: 0}); !in {
		t.Fatal("the pole itself is not inside a ring around it")
	}
}

// TestConcaveNotchCellsAreFilteredByTheExactCheck pins the reason the exact check exists: the
// covering of a U contains the cells of its notch, so a covering hit is not containment.
func TestConcaveNotchCellsAreFilteredByTheExactCheck(t *testing.T) {
	u := mustPolygon(t, ring(
		[2]float64{0, 0}, [2]float64{0.30, 0}, [2]float64{0.30, 0.30}, [2]float64{0.2005, 0.30},
		[2]float64{0.2005, 0.05}, [2]float64{0.1995, 0.05}, [2]float64{0.1995, 0.30}, [2]float64{0, 0.30}))
	notch := geo.Position{Lat: 0.20, Lon: 0.2}
	if in, _ := geo.Contains([]*geo.Polygon{u}, notch); in {
		t.Fatal("the notch point is inside the U; the fixture is wrong")
	}
	ranges, err := geo.CoverPolygonSet([]*geo.Polygon{u}, geo.MaxCoverRanges)
	if err != nil {
		t.Fatal(err)
	}
	cell, _ := geo.LeafCell(notch)
	if !geo.InRanges(ranges, cell) {
		t.Fatal("the notch is not in the covering, so this fixture does not exercise the exact check")
	}
	rng := rand.New(rand.NewSource(5))
	cands := append(jitter(rng, "u", 0.15, 0.15, 0.2, 800), installedAt("notch", notch.Lat, notch.Lon))
	hits := agree(t, "concave", cands, within(t, u), geo.ModeInstalled)
	for _, h := range hits {
		if h.Token == "notch" {
			t.Fatal("the notch device was reported inside the U")
		}
	}
}

func TestPolygonWithHole(t *testing.T) {
	donut := mustPolygon(t, square(10, 10, 1), square(10, 10, 0.4))
	cands := []geo.Candidate{
		installedAt("centre", 10, 10),            // in the hole: out
		installedAt("ring", 10.7, 10),            // in the band: in
		installedAt("holeedge", 10.4, 10.4),      // a hole vertex, on its boundary: in
		installedAt("outer", 10, 11),             // on the exterior boundary: in
		installedAt("outside", 12, 12),           // out
		installedAt("holeinner", 10.39999, 10.0), // strictly inside the hole: out
	}
	rng := rand.New(rand.NewSource(6))
	cands = append(cands, jitter(rng, "d", 10, 10, 1.5, 800)...)
	hits := agree(t, "hole", cands, within(t, donut), geo.ModeInstalled)
	got := map[string]bool{}
	for _, h := range hits {
		got[h.Token] = true
	}
	for tok, want := range map[string]bool{"centre": false, "ring": true, "holeedge": true, "outer": true, "outside": false, "holeinner": false} {
		if got[tok] != want {
			t.Errorf("%s: in=%v, want %v", tok, got[tok], want)
		}
	}
}

func TestOverlappingPolygons(t *testing.T) {
	a := mustPolygon(t, square(40, -100, 0.5))
	b := mustPolygon(t, square(40.3, -99.7, 0.5)) // overlaps a
	rng := rand.New(rand.NewSource(7))
	cands := jitter(rng, "o", 40.15, -99.85, 1.0, 1000)
	hits := agree(t, "overlap", cands, within(t, a, b), geo.ModeInstalled)
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.Token] {
			t.Fatalf("%s returned twice", h.Token)
		}
		seen[h.Token] = true
	}
	ra, _ := geo.CoverPolygonSet([]*geo.Polygon{a, b}, geo.MaxCoverRanges)
	for i := 1; i < len(ra); i++ {
		if ra[i].Lo <= ra[i-1].Hi+2 {
			t.Fatalf("ranges overlap or touch after merge: %v", ra)
		}
	}
}

func TestBoundaryBandIsCovered(t *testing.T) {
	poly := mustPolygon(t, square(0, 0, 1))
	// 4 mm outside the eastern edge: inside the 6 mm band, so Contains says yes and the covering
	// must therefore reach it even though the loop does not.
	p := geo.Position{Lat: 0, Lon: 1 + 4e-3/geo.EarthRadiusM*180/math.Pi}
	in, err := geo.Contains([]*geo.Polygon{poly}, p)
	if err != nil || !in {
		t.Fatalf("a point 4 mm outside the edge should be inside the boundary band (in=%v err=%v)", in, err)
	}
	ranges, err := geo.CoverPolygonSet([]*geo.Polygon{poly}, geo.MaxCoverRanges)
	if err != nil {
		t.Fatal(err)
	}
	cell, _ := geo.LeafCell(p)
	if !geo.InRanges(ranges, cell) {
		t.Fatal("the covering misses a point the exact check accepts")
	}
}

func TestCapBoundaryIsInclusive(t *testing.T) {
	c := geo.Position{Lat: 10, Lon: 20}
	p := geo.Position{Lat: 10.01, Lon: 20.01}
	d, err := geo.DistanceM(c, p)
	if err != nil {
		t.Fatal(err)
	}
	cands := []geo.Candidate{installedAt("exact", p.Lat, p.Lon)}
	exact, _ := geo.Near(c, d)
	if got := agree(t, "cap exact", cands, exact, geo.ModeInstalled); len(got) != 1 {
		t.Fatalf("a position at exactly the radius must be included, got %d", len(got))
	}
	short, _ := geo.Near(c, math.Nextafter(d, 0))
	if got := agree(t, "cap short", cands, short, geo.ModeInstalled); len(got) != 0 {
		t.Fatal("a position beyond the radius must be excluded")
	}
}

func TestDistanceM(t *testing.T) {
	d, err := geo.DistanceM(geo.Position{}, geo.Position{Lon: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := geo.EarthRadiusM * math.Pi / 180
	if math.Abs(d-want) > 1e-6 {
		t.Fatalf("one degree of equator = %v m, want %v", d, want)
	}
	if _, err := geo.DistanceM(geo.Position{Lat: 91}, geo.Position{}); !errors.Is(err, geo.ErrInvalidPosition) {
		t.Fatalf("an out-of-range latitude must be refused, got %v", err)
	}
}

func TestSelfIntersectingRingIsRefused(t *testing.T) {
	bow := ring([2]float64{0, 0}, [2]float64{1, 1}, [2]float64{1, 0}, [2]float64{0, 1})
	if _, err := geo.NewPolygon([][][]float64{bow}); err == nil {
		t.Fatal("a bow-tie was accepted")
	}
	// A hole that is a bow-tie is refused too.
	if _, err := geo.NewPolygon([][][]float64{square(0, 0, 5), bow}); err == nil {
		t.Fatal("a bow-tie hole was accepted")
	}
	if _, err := geo.NewPolygon(nil); err == nil {
		t.Fatal("a polygon with no rings was accepted")
	}
}

func TestErrTooComplex(t *testing.T) {
	// Vertices: a 1,001-vertex ring is over the 1,000 cap.
	var big [][2]float64
	for i := 0; i < 1001; i++ {
		a := 2 * math.Pi * float64(i) / 1001
		big = append(big, [2]float64{0.5 * math.Cos(a), 0.5 * math.Sin(a)})
	}
	if _, err := geo.CoverPolygonSet([]*geo.Polygon{mustPolygon(t, ring(big...))}, geo.MaxCoverRanges); !errors.Is(err, geo.ErrTooComplex) {
		t.Fatalf("1001 vertices: got %v, want ErrTooComplex", err)
	}
	// ... and 1,000 is accepted: the cap is a boundary, not a margin.
	if _, err := geo.CoverPolygonSet([]*geo.Polygon{mustPolygon(t, ring(big[:1000]...))}, geo.MaxCoverRanges); err != nil {
		t.Fatalf("1000 vertices refused: %v", err)
	}
	// Ranges: 300 small squares scattered over the globe cannot merge into 256 ranges.
	var set []*geo.Polygon
	for i := 0; i < 300; i++ {
		set = append(set, mustPolygon(t, square(-60+0.4*float64(i%300), -170+1.1*float64(i), 0.01)))
	}
	if _, err := geo.CoverPolygonSet(set, geo.MaxCoverRanges); !errors.Is(err, geo.ErrTooComplex) {
		t.Fatalf("300 scattered polygons: got %v, want ErrTooComplex", err)
	}
	// A caller-chosen cap is honoured: one range cannot hold a large cap.
	if _, err := geo.CoverCap(geo.Position{Lat: 35.26, Lon: -135}, 500_000, 1); !errors.Is(err, geo.ErrTooComplex) {
		t.Fatalf("a large cap into one range: got %v, want ErrTooComplex", err)
	}
	// Malformed arguments are not complexity.
	for _, n := range []int{0, -1, geo.MaxCoverRanges + 1} {
		if _, err := geo.CoverCap(geo.Position{}, 100, n); !errors.Is(err, geo.ErrInvalidArgument) {
			t.Fatalf("maxRanges %d: got %v, want ErrInvalidArgument", n, err)
		}
	}
	if _, err := geo.CoverPolygonSet(nil, 10); !errors.Is(err, geo.ErrInvalidArgument) {
		t.Fatalf("empty set: got %v", err)
	}
}

func TestEffective(t *testing.T) {
	inst := &geo.Position{Lat: 1, Lon: 2}
	fresh := &geo.Fix{Position: geo.Position{Lat: 3, Lon: 4}, At: t0.Add(-time.Minute)}
	stale := &geo.Fix{Position: geo.Position{Lat: 3, Lon: 4}, At: t0.Add(-time.Hour)}
	future := &geo.Fix{Position: geo.Position{Lat: 3, Lon: 4}, At: t0.Add(time.Minute)}
	corrupt := &geo.Fix{Position: geo.Position{Lat: math.NaN(), Lon: 4}, At: t0}
	bound := 10 * time.Minute

	cases := []struct {
		name     string
		reported *geo.Fix
		fresh    time.Duration
		inst     *geo.Position
		mode     geo.Mode
		want     geo.Source
		wantPos  geo.Position
	}{
		{"installed ignores a fresh fix", fresh, bound, inst, geo.ModeInstalled, geo.SourceInstalled, *inst},
		{"installed with nothing installed is none", fresh, bound, nil, geo.ModeInstalled, geo.SourceNone, geo.Position{}},
		{"effective prefers a fresh fix", fresh, bound, inst, geo.ModeEffective, geo.SourceReported, fresh.Position},
		{"effective falls back when stale", stale, bound, inst, geo.ModeEffective, geo.SourceInstalled, *inst},
		{"effective: a future fix is not yet known", future, bound, inst, geo.ModeEffective, geo.SourceInstalled, *inst},
		{"effective: no freshness bound means no reported", fresh, 0, inst, geo.ModeEffective, geo.SourceInstalled, *inst},
		{"effective skips a corrupt fix", corrupt, bound, inst, geo.ModeEffective, geo.SourceInstalled, *inst},
		{"effective with neither is none", nil, bound, nil, geo.ModeEffective, geo.SourceNone, geo.Position{}},
		{"reported returns a stale fix", stale, bound, inst, geo.ModeReported, geo.SourceReported, stale.Position},
		{"reported never falls back to installed", nil, bound, inst, geo.ModeReported, geo.SourceNone, geo.Position{}},
	}
	for _, tc := range cases {
		pos, src, ok := geo.Effective(tc.reported, tc.fresh, tc.inst, t0, tc.mode)
		if src != tc.want || ok != (tc.want != geo.SourceNone) || pos != tc.wantPos {
			t.Errorf("%s: got (%v, %v, %v), want (%v, %v)", tc.name, pos, src, ok, tc.wantPos, tc.want)
		}
	}
	// A freshness bound is inclusive.
	edge := &geo.Fix{Position: geo.Position{Lat: 3, Lon: 4}, At: t0.Add(-bound)}
	if _, src, _ := geo.Effective(edge, bound, inst, t0, geo.ModeEffective); src != geo.SourceReported {
		t.Error("a fix exactly at the freshness bound should be fresh")
	}
	defer func() {
		if recover() == nil {
			t.Error("an invalid mode must fail loudly")
		}
	}()
	geo.Effective(fresh, bound, inst, t0, geo.ModeUnset)
}

// TestInstalledQueryNeverSeesALiveFix is the end-to-end form of the authority rule: a device that
// reports GPS far away from where it is installed is found at its installed position in INSTALLED
// mode, and its live position never decides the answer.
func TestInstalledQueryNeverSeesALiveFix(t *testing.T) {
	fixed := &geo.Position{Lat: 50, Lon: 8}
	live := &geo.Fix{Position: geo.Position{Lat: -33, Lon: 151}, At: t0.Add(-time.Second)}
	cands := []geo.Candidate{{Token: "lamp", Reported: live, Freshness: time.Hour, Installed: fixed}}
	here := within(t, mustPolygon(t, square(50, 8, 0.1)))
	there := within(t, mustPolygon(t, square(-33, 151, 0.1)))

	if got := agree(t, "installed here", cands, here, geo.ModeInstalled); len(got) != 1 || got[0].Source != geo.SourceInstalled {
		t.Fatalf("installed mode must find the lamp where it is installed: %+v", got)
	}
	if got := agree(t, "installed there", cands, there, geo.ModeInstalled); len(got) != 0 {
		t.Fatalf("installed mode found the lamp at its live fix: %+v", got)
	}
	if got := agree(t, "effective there", cands, there, geo.ModeEffective); len(got) != 1 || got[0].Source != geo.SourceReported {
		t.Fatalf("effective mode must follow the fresh fix: %+v", got)
	}
	if got := agree(t, "effective here", cands, here, geo.ModeEffective); len(got) != 0 {
		t.Fatalf("effective mode must not also answer from the installed position: %+v", got)
	}
}

func TestQueryOrderingDedupeAndBounds(t *testing.T) {
	c := geo.Position{Lat: 0, Lon: 0}
	cands := []geo.Candidate{
		installedAt("b", 0, 0.001), installedAt("a", 0, -0.001), // equidistant: token breaks the tie
		installedAt("z", 0, 0.0001),
		installedAt("b", 0, 0.001), // exact duplicate: dropped
	}
	near, _ := geo.Near(c, 1000)
	got, err := geo.Query(seq(cands), near, geo.ModeInstalled, t0)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range got {
		order = append(order, r.Token)
	}
	if !reflect.DeepEqual(order, []string{"z", "a", "b"}) {
		t.Fatalf("near order = %v, want [z a b]", order)
	}
	poly := within(t, mustPolygon(t, square(0, 0, 1)))
	got, _ = geo.Query(seq(cands), poly, geo.ModeInstalled, t0)
	order = order[:0]
	for _, r := range got {
		order = append(order, r.Token)
	}
	if !reflect.DeepEqual(order, []string{"a", "b", "z"}) {
		t.Fatalf("within order = %v, want [a b z]", order)
	}

	conflict := []geo.Candidate{installedAt("x", 0, 0), installedAt("x", 1, 1)}
	if _, err := geo.Query(seq(conflict), poly, geo.ModeInstalled, t0); !errors.Is(err, geo.ErrConflictingCandidate) {
		t.Fatalf("conflicting duplicates: got %v", err)
	}
	over := func(yield func(geo.Candidate) bool) {
		for i := 0; i <= geo.DefaultMaxCandidates; i++ {
			if !yield(installedAt(fmt.Sprintf("t%06d", i), 0, 0)) {
				return
			}
		}
	}
	if got, err := geo.Query(over, poly, geo.ModeInstalled, t0); !errors.Is(err, geo.ErrLimitExceeded) || got != nil {
		t.Fatalf("an oversize candidate set must fail with no partial results, got %d results and %v", len(got), err)
	}
	if _, err := geo.Query(seq(cands), poly, geo.ModeUnset, t0); !errors.Is(err, geo.ErrInvalidArgument) {
		t.Fatalf("unset mode: got %v", err)
	}
	if _, err := geo.Query(seq(cands), geo.Region{}, geo.ModeInstalled, t0); !errors.Is(err, geo.ErrInvalidArgument) {
		t.Fatalf("zero region: got %v", err)
	}
}

func seq(c []geo.Candidate) func(func(geo.Candidate) bool) {
	return func(yield func(geo.Candidate) bool) {
		for _, x := range c {
			if !yield(x) {
				return
			}
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"INSTALLED", "REPORTED", "EFFECTIVE"} {
		m, err := geo.ParseMode(s)
		if err != nil || m.String() != s {
			t.Errorf("ParseMode(%q) = %v, %v", s, m, err)
		}
	}
	if _, err := geo.ParseMode("installed"); err == nil {
		t.Error("a lower-case mode was accepted")
	}
}

// TestFuzzIndexedEqualsBruteForce compares the whole indexed path with brute force on random
// polygons and radii anywhere on the sphere (poles, antimeridian and every face included), with
// random mixes of installed and reported positions, freshness and modes, plus points planted on
// vertices and edge midpoints to stress the boundary convention.
func TestFuzzIndexedEqualsBruteForce(t *testing.T) {
	cases := 12000
	if testing.Short() {
		cases = 300
	}
	rng := rand.New(rand.NewSource(20261107))
	skipped, within_, nonEmpty := 0, 0, 0
	for i := 0; i < cases; i++ {
		lat := math.Asin(rng.Float64()*2-1) * 180 / math.Pi
		lon := rng.Float64()*360 - 180
		scale := math.Pow(10, -3+3*rng.Float64()) // 0.001 .. 1 degree
		var region geo.Region
		var verts []geo.Position
		if rng.Intn(3) == 0 {
			r, err := geo.Near(geo.Position{Lat: lat, Lon: lon}, scale*111_000)
			if err != nil {
				t.Fatal(err)
			}
			region = r
		} else {
			var set []*geo.Polygon
			for k := 1 + rng.Intn(3); k > 0; k-- {
				coords := starRing(rng, lat+(rng.Float64()-0.5)*scale, lon+(rng.Float64()-0.5)*scale, scale)
				p, err := geo.NewPolygon([][][]float64{coords})
				if err != nil {
					skipped++
					continue
				}
				set = append(set, p)
				for _, c := range coords {
					verts = append(verts, geo.Position{Lat: c[1], Lon: c[0]})
				}
			}
			if len(set) == 0 {
				continue
			}
			r, err := geo.Within(set)
			if err != nil {
				t.Fatal(err)
			}
			region = r
			within_++
		}
		cands := jitter(rng, "c", lat, lon, scale*1.6, 40)
		for j, v := range verts {
			if j >= 10 {
				break
			}
			cands = append(cands, installedAt(fmt.Sprintf("v-%d", j), v.Lat, v.Lon))
		}
		for j := range cands {
			switch rng.Intn(4) {
			case 0: // reported only
				cands[j].Reported = &geo.Fix{Position: *cands[j].Installed, At: t0.Add(-time.Duration(rng.Intn(120)) * time.Minute)}
				cands[j].Installed = nil
				cands[j].Freshness = 30 * time.Minute
			case 1: // both, at different places
				cands[j].Reported = &geo.Fix{
					Position: geo.Position{Lat: math.Max(-90, math.Min(90, lat+(rng.Float64()-0.5)*scale)), Lon: wrapLon(lon + (rng.Float64()-0.5)*scale)},
					At:       t0.Add(-time.Duration(rng.Intn(120)) * time.Minute)}
				cands[j].Freshness = 30 * time.Minute
			}
		}
		mode := []geo.Mode{geo.ModeInstalled, geo.ModeReported, geo.ModeEffective}[rng.Intn(3)]
		got := agree(t, fmt.Sprintf("case %d", i), cands, region, mode)
		if len(got) > 0 {
			nonEmpty++
		}
	}
	t.Logf("%d cases (%d polygon sets, %d rings refused as self-intersecting), %d with a non-empty answer", cases, within_, skipped, nonEmpty)
	if nonEmpty < cases/4 {
		t.Fatalf("only %d of %d cases had a non-empty answer; the fuzz is not exercising matches", nonEmpty, cases)
	}
}

// starRing is a random star-shaped ring: sorted angles with random radii. It is usually simple, and
// NewPolygon refuses the occasional one that is not, which the caller counts.
func starRing(rng *rand.Rand, lat, lon, scale float64) [][]float64 {
	n := 4 + rng.Intn(20)
	angles := make([]float64, n)
	for i := range angles {
		angles[i] = rng.Float64() * 2 * math.Pi
	}
	sortFloats(angles)
	out := make([][]float64, 0, n+1)
	for _, a := range angles {
		r := scale * (0.3 + 0.7*rng.Float64())
		la := math.Max(-90, math.Min(90, lat+r*math.Sin(a)))
		out = append(out, []float64{wrapLon(lon + r*math.Cos(a)), la})
	}
	return append(out, []float64{out[0][0], out[0][1]})
}

func sortFloats(a []float64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// TestBoundaryBandIsCoveredAtCellEdges is the test that makes the neighbour padding load-bearing.
// A polygon that IS an S2 cell has the covering cell's own edge as its boundary, so a point a few
// millimetres outside that edge lies in the NEIGHBOUR cell: without the padding the covering
// misses a point the exact check accepts, which the square-on-a-meridian fixture above cannot show
// because its covering cells overhang the polygon on every side.
func TestBoundaryBandIsCoveredAtCellEdges(t *testing.T) {
	checked := 0
	for _, probe := range []struct {
		level int
		lat   float64
		lon   float64
	}{{8, 10, 20}, {12, 47, 8}, {14, -33, 151}, {10, 80, 10}, {10, 0, -135}, {16, 35.2, -135}, {9, -80, 10}, {11, 0, 180}} {
		id := s2.CellIDFromLatLng(s2.LatLngFromDegrees(probe.lat, probe.lon)).Parent(probe.level)
		cell := s2.CellFromCellID(id)
		var coords [][]float64
		for i := 0; i < 4; i++ {
			ll := s2.LatLngFromPoint(cell.Vertex(i))
			coords = append(coords, []float64{ll.Lng.Degrees(), ll.Lat.Degrees()})
		}
		coords = append(coords, coords[0])
		poly := mustPolygon(t, coords)
		ranges, err := geo.CoverPolygonSet([]*geo.Polygon{poly}, geo.MaxCoverRanges)
		if err != nil {
			t.Fatal(err)
		}
		centre := cell.Center()
		for i := 0; i < 4; i++ {
			mid := s2.Point{Vector: cell.Vertex(i).Add(cell.Vertex((i + 1) % 4).Vector).Normalize()}
			out := mid.Sub(centre.Vector).Normalize()
			p := s2.Point{Vector: mid.Add(out.Mul(4e-3 / geo.EarthRadiusM)).Normalize()}
			ll := s2.LatLngFromPoint(p)
			pos := geo.Position{Lat: ll.Lat.Degrees(), Lon: ll.Lng.Degrees()}
			if poly.Exterior.Loop.ContainsPoint(p) {
				t.Fatalf("level %d edge %d: the probe is inside the loop; the fixture proves nothing", probe.level, i)
			}
			in, err := geo.Contains([]*geo.Polygon{poly}, pos)
			if err != nil || !in {
				t.Fatalf("level %d edge %d: a point 4 mm outside the edge must be inside the band (in=%v err=%v)", probe.level, i, in, err)
			}
			cellValue, _ := geo.LeafCell(pos)
			if !geo.InRanges(ranges, cellValue) {
				t.Fatalf("level %d edge %d: the covering misses a point the exact check accepts", probe.level, i)
			}
			checked++
		}
	}
	if checked != 32 {
		t.Fatalf("checked %d probes", checked)
	}
}

// TestDuplicateWithACorruptCoordinateIsStillADuplicate: the same candidate yielded twice, one
// coordinate NaN, is a duplicate and not a conflict, and its valid installed position still answers.
func TestDuplicateWithACorruptCoordinateIsStillADuplicate(t *testing.T) {
	c := geo.Candidate{
		Token:     "x",
		Installed: &geo.Position{Lat: 0, Lon: 0},
		Reported:  &geo.Fix{Position: geo.Position{Lat: math.NaN(), Lon: 1}, At: t0},
		Freshness: time.Hour,
	}
	region := within(t, mustPolygon(t, square(0, 0, 1)))
	for _, mode := range []geo.Mode{geo.ModeInstalled, geo.ModeEffective} {
		got, err := geo.Query(seq([]geo.Candidate{c, c}), region, mode, t0)
		if err != nil || len(got) != 1 || got[0].Source != geo.SourceInstalled {
			t.Fatalf("%v: got %+v, %v", mode, got, err)
		}
	}
}
