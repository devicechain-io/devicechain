// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/geo"
)

// The pit geofence is a contract with the Unity scene's dashed outline, so what is pinned
// here is pinned against LITERALS and an independently written conversion, never against
// a helper the ring was produced by.

// The site origin, spelled out a second time on purpose. A rename or a shift of the one in
// sitepulse_geofence.go must be typed twice, and the second typing is a reviewable diff;
// TestSitepulseOriginIsTheDocumentedOne compares the two.
const (
	wantOriginLat = 39.0
	wantOriginLon = -117.0
	// Earth radius of the small-area conversion.
	wantEarthRadiusM = 6371000.0
)

// siteToLonLat is the small-area conversion, written here from its formula:
//
//	latitude  = originLat + north/R * 180/pi
//	longitude = originLon + east/(R*cos(originLat)) * 180/pi
func siteToLonLat(originLat, originLon, east, north float64) [2]float64 {
	lat := originLat + north/wantEarthRadiusM*180/math.Pi
	lon := originLon + east/(wantEarthRadiusM*math.Cos(originLat*math.Pi/180))*180/math.Pi
	return [2]float64{lon, lat}
}

type pitRimFile struct {
	Points [][2]float64 `json:"points"`
}

func loadPitRim(t *testing.T) [][2]float64 {
	t.Helper()
	raw, err := os.ReadFile("testdata/sitepulse_pit_rim.json")
	if err != nil {
		t.Fatal(err)
	}
	var f pitRimFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Points
}

func pitFence(t *testing.T) GeoFenceSpec {
	t.Helper()
	m := sitepulseManifest(t)
	for _, g := range m.GeoFences {
		if g.Token == "sp-geofence-pit" {
			return g
		}
	}
	t.Fatalf("the sitepulse manifest declares no geofence %q (it declares %+v)", "sp-geofence-pit", m.GeoFences)
	return GeoFenceSpec{}
}

func TestSitepulseProvisionsThePitGeofence(t *testing.T) {
	m := sitepulseManifest(t)
	if len(m.GeoFences) != 1 {
		t.Fatalf("the site declares %d geofences, want exactly 1 (the pit): %+v", len(m.GeoFences), m.GeoFences)
	}
	g := pitFence(t)
	if g.Name != "Pit" {
		t.Errorf("the fence is named %q, want %q", g.Name, "Pit")
	}
	if !strings.Contains(g.Description, "sp-zone-cut") {
		t.Errorf("the description must say which site zone the pit is in, since the platform has "+
			"no first-class fence-to-area link: %q", g.Description)
	}
	// The fence is NOT a zone: it must not appear among the areas (which the goto enum is
	// derived from) under any spelling.
	for _, a := range m.Areas {
		if a.Token == g.Token {
			t.Errorf("the pit fence %q is also declared as an area, which would put it in the goto enum", g.Token)
		}
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("the sitepulse manifest with its fence does not validate: %v", err)
	}
}

func TestSitepulsePitGeofenceIsTheSceneRimAtTheSiteOrigin(t *testing.T) {
	pts := loadPitRim(t)
	if len(pts) != 160 {
		t.Fatalf("the committed scene rim has %d points, want 160", len(pts))
	}
	ring := pitFence(t).Ring
	if len(ring) != len(pts)+1 {
		t.Fatalf("the ring has %d positions, want %d (160 rim points plus the closing repeat)", len(ring), len(pts)+1)
	}
	if ring[0] != ring[len(ring)-1] {
		t.Errorf("the ring is not closed: first %v, last %v", ring[0], ring[len(ring)-1])
	}

	const tolDeg = 6e-8 // 7-decimal rounding is at most 5e-8
	var mismatches []string
	for i := range ring {
		p := pts[i%len(pts)]
		want := siteToLonLat(wantOriginLat, wantOriginLon, p[0], p[1])
		e := math.Max(math.Abs(ring[i][0]-want[0]), math.Abs(ring[i][1]-want[1]))
		if e > tolDeg {
			// Print the line the literal should carry, so this test is the regenerator.
			mismatches = append(mismatches, fmt.Sprintf("\t{%.7f, %.7f}, // x=%v z=%v   (have {%.7f, %.7f})",
				want[0], want[1], p[0], p[1], ring[i][0], ring[i][1]))
		}
	}
	if len(mismatches) > 0 {
		t.Fatalf("%d ring positions are not the scene's rim at origin (%v, %v); expected lines:\n%s",
			len(mismatches), wantOriginLat, wantOriginLon, strings.Join(mismatches, "\n"))
	}

	// Negative control: the tolerance must be able to see a 1 m error, or the pass above
	// proves nothing about the origin.
	shifted := 0.0
	for i := range ring {
		p := pts[i%len(pts)]
		want := siteToLonLat(wantOriginLat, wantOriginLon, p[0], p[1]+1)
		shifted = math.Max(shifted, math.Max(math.Abs(ring[i][0]-want[0]), math.Abs(ring[i][1]-want[1])))
	}
	if shifted < 10*tolDeg {
		t.Errorf("a 1 m shift moves the ring by only %g deg, under 10x the %g tolerance", shifted, tolDeg)
	}
}

func TestSitepulsePitGeofenceContainsThePitAndNotTheYard(t *testing.T) {
	ring := pitFence(t).Ring
	slices := make([][]float64, len(ring))
	for i := range ring {
		slices[i] = ring[i][:]
	}
	loop, err := geo.LoopFromClosedRing(slices)
	if err != nil {
		t.Fatalf("the pit ring does not compile in the engine's builder: %v", err)
	}
	// Site metres (east, north). PointFromDegrees is LATITUDE first; the ring is lon first.
	at := func(east, north float64) bool {
		ll := siteToLonLat(wantOriginLat, wantOriginLon, east, north)
		return loop.ContainsPoint(geo.PointFromDegrees(ll[1], ll[0]))
	}
	inside := map[string][2]float64{
		"load point": {4, 35.5}, "muck pile": {4, 56.5}, "pit stockpile": {50, 46},
		"cut zone corner sw": {-38, 20}, "cut zone corner ne": {62, 56},
		"cut zone corner nw": {-38, 56}, "cut zone corner se": {62, 20},
	}
	outside := map[string][2]float64{
		"refuel bay": {-59.5, -56.5}, "crusher plant": {-34, -100}, "fill pad": {93, -55},
		"yard": {-77, -30},
	}
	for name, p := range inside {
		if !at(p[0], p[1]) {
			t.Errorf("%s (%v) is not inside the pit fence", name, p)
		}
	}
	for name, p := range outside {
		if at(p[0], p[1]) {
			t.Errorf("%s (%v) is inside the pit fence", name, p)
		}
	}
	// A lat/lon swap must move a probe across the boundary, or the order is unobserved.
	ll := siteToLonLat(wantOriginLat, wantOriginLon, 4, 35.5)
	if loop.ContainsPoint(geo.PointFromDegrees(ll[0], ll[1])) {
		t.Error("a latitude/longitude swap still reads inside; the probe cannot see the order")
	}
}

func TestSitepulsePitGeofenceDocumentIsLonLatPolygon2D(t *testing.T) {
	doc, err := pitFence(t).geometryDocument()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Kind     string `json:"kind"`
		Geometry struct {
			Type        string        `json:"type"`
			Coordinates [][][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatalf("the document is not JSON: %v", err)
	}
	if d.Kind != "POLYGON_2D" || d.Geometry.Type != "Polygon" {
		t.Fatalf("kind %q type %q, want POLYGON_2D Polygon", d.Kind, d.Geometry.Type)
	}
	if len(d.Geometry.Coordinates) != 1 {
		t.Fatalf("%d rings, want exactly the exterior ring", len(d.Geometry.Coordinates))
	}
	// The document must carry exactly the validated ring, closed and in order.
	got := d.Geometry.Coordinates[0]
	ring := pitFence(t).Ring
	if len(got) != len(ring) {
		t.Fatalf("document ring has %d positions, the spec's ring %d", len(got), len(ring))
	}
	for i, p := range got {
		if len(p) != 2 || p[0] != ring[i][0] || p[1] != ring[i][1] {
			t.Fatalf("document position %d is %v, the spec's ring has %v", i, p, ring[i])
		}
	}
	if err := geo.ValidateClosedRing(got); err != nil {
		t.Errorf("the document's ring is not a usable closed ring: %v", err)
	}
	first := got[0]
	if math.Abs(first[0]-(-117)) > 0.01 || math.Abs(first[1]-39) > 0.01 {
		t.Errorf("first position %v is not [lon, lat] near (-117, 39)", first)
	}
}

// Validate refuses an unusable fence, each error naming the fence token.
func TestValidateRefusesAnUnusableGeofence(t *testing.T) {
	square := func() [][2]float64 {
		return [][2]float64{{-117, 39}, {-116.999, 39}, {-116.999, 39.001}, {-117, 39.001}, {-117, 39}}
	}
	with := func(g GeoFenceSpec) SimManifest {
		m := testManifest()
		m.GeoFences = []GeoFenceSpec{g}
		return m
	}
	ok := GeoFenceSpec{Token: "gf-ok", Name: "Ok", Ring: square()}
	if err := with(ok).Validate(); err != nil {
		t.Fatalf("control: a valid square was refused: %v", err)
	}

	cases := []struct {
		name string
		m    SimManifest
	}{
		{"bad token", with(GeoFenceSpec{Token: "bad token", Name: "x", Ring: square()})},
		{"empty name", with(GeoFenceSpec{Token: "gf-ok", Ring: square()})},
		{"three positions", with(GeoFenceSpec{Token: "gf-ok", Name: "x", Ring: [][2]float64{{-117, 39}, {-116.9, 39}, {-117, 39}}})},
		{"unclosed", with(GeoFenceSpec{Token: "gf-ok", Name: "x", Ring: square()[:4]})},
		{"bow-tie", with(GeoFenceSpec{Token: "gf-ok", Name: "x", Ring: [][2]float64{
			{-117, 39}, {-116.999, 39.001}, {-116.999, 39}, {-117, 39.001}, {-117, 39}}})},
		{"latitude 91", with(GeoFenceSpec{Token: "gf-ok", Name: "x", Ring: [][2]float64{
			{-117, 91}, {-116.9, 91}, {-116.9, 90}, {-117, 90}, {-117, 91}}})},
		{"duplicate token", func() SimManifest {
			m := with(ok)
			m.GeoFences = append(m.GeoFences, ok)
			return m
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if err == nil {
				t.Fatal("an unusable fence validated")
			}
			if c.name != "bad token" && !strings.Contains(err.Error(), "gf-ok") {
				t.Errorf("the error does not name the fence: %v", err)
			}
		})
	}
}

// The origin the Unity scene is told to adopt is the constants in sitepulse_geofence.go;
// the ring tests use their own copy, so this is the one place the two are compared.
func TestSitepulseOriginIsTheDocumentedOne(t *testing.T) {
	if sitepulseOriginLatitude != wantOriginLat || sitepulseOriginLongitude != wantOriginLon {
		t.Fatalf("origin constants are (%v, %v), the documented origin is (%v, %v)",
			sitepulseOriginLatitude, sitepulseOriginLongitude, wantOriginLat, wantOriginLon)
	}
}
