// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harborSceneFixture is the layout file the Unity showcase loads, relative to this package. It is
// outside this module, so `go test` does not track it: run with -count=1 after editing it.
var harborSceneFixture = filepath.Join("..", "..", "..", "..", "demos", "harborpulse-unity", "Assets", "Harborpulse", "Resources", "harbor_layout.json")

func harborSceneJSON(t *testing.T) []byte {
	t.Helper()
	b, err := HarborpulseSceneLayoutJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestHarborpulseSceneLayoutFixture holds the committed scene layout to the manifest. Regenerate
// with HARBORPULSE_UPDATE_SCENE=1 go test ./sim -run TestHarborpulseSceneLayoutFixture -count=1.
func TestHarborpulseSceneLayoutFixture(t *testing.T) {
	want := harborSceneJSON(t)
	if os.Getenv("HARBORPULSE_UPDATE_SCENE") == "1" {
		if err := os.WriteFile(harborSceneFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", harborSceneFixture)
		return
	}
	got, err := os.ReadFile(harborSceneFixture)
	if err != nil {
		t.Fatalf("read the committed scene layout: %v", err)
	}
	// git on Windows may check the file out with CRLF line endings; the content is what matters
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not the layout the manifest produces; regenerate it with HARBORPULSE_UPDATE_SCENE=1 (see the test's comment)", harborSceneFixture)
	}
}

// TestHarborpulseSceneLayoutIsTheManifestFleet: every device the scene draws is a device the
// manifest provisions, under the same external ID and type, and the other way round.
func TestHarborpulseSceneLayoutIsTheManifestFleet(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	provisioned := map[string]string{}
	for _, p := range m.Populations {
		for _, in := range p.Instances {
			provisioned[in.ExternalId] = p.OfType
		}
	}
	// Nothing the manifest provisions is left undrawn (the S6 chargers and pull lamps were, until the
	// scene authored their anchors): the two sets below are compared whole, in both directions.
	layout := HarborpulseSceneLayout()
	drawn := map[string]string{}
	for _, d := range layout.Devices {
		if _, dup := drawn[d.ExternalId]; dup {
			t.Fatalf("the scene draws %s twice", d.ExternalId)
		}
		drawn[d.ExternalId] = d.DeviceType
	}
	if len(drawn) != len(provisioned) {
		t.Fatalf("the scene draws %d devices, the manifest provisions %d", len(drawn), len(provisioned))
	}
	for id, typ := range provisioned {
		if drawn[id] != typ {
			t.Errorf("%s: the manifest provisions type %q, the scene draws %q", id, typ, drawn[id])
		}
	}

	// every light carries the position the manifest gives it, and nothing else carries one
	for _, d := range layout.Devices {
		pos, isLight := HarborpulseLampPosition(d.Token)
		switch {
		case isLight && (d.Position == nil || d.Position.Lat != pos.Latitude || d.Position.Lon != pos.Longitude):
			t.Errorf("%s: the scene's position %+v is not the manifest's %+v", d.Token, d.Position, pos)
		case !isLight && d.Position != nil:
			t.Errorf("%s: the scene gives a position the manifest does not", d.Token)
		}
	}

	// the S6 populations are drawn as their own kinds, never as a district charger or street light:
	// the scene binds one device type to a kind and reads that kind's metrics
	kinds := map[string]string{HarborpulseMQTTChargerType: harborSceneMQTTCharger, HarborpulseX509ChargerType: harborSceneCertCharger, HarborpulsePullLightType: harborScenePullLight}
	typeOf := map[string]string{}
	s6 := 0
	for _, d := range layout.Devices {
		if want, ok := kinds[d.DeviceType]; ok {
			s6++
			if d.Kind != want {
				t.Errorf("%s: type %s is drawn as kind %q, want %q", d.Token, d.DeviceType, d.Kind, want)
			}
		}
		if other, seen := typeOf[d.Kind]; seen && other != d.DeviceType {
			t.Errorf("kind %q is drawn for two device types, %s and %s", d.Kind, other, d.DeviceType)
		}
		typeOf[d.Kind] = d.DeviceType
	}
	if want := len(HarborpulseMQTTChargers()) + len(HarborpulseX509Chargers()) + len(HarborpulsePullLights()); s6 != want {
		t.Errorf("the scene draws %d S6 devices, the scenario has %d", s6, want)
	}

	// the beats are the ones /status names
	if len(layout.Beats) != len(harborBeats) {
		t.Errorf("the scene has %d beats, the scenario %d", len(layout.Beats), len(harborBeats))
	}
	for _, b := range layout.Beats {
		if !strings.Contains(","+harborBeatAt((b.From+b.To)/2)+",", ","+b.Name+",") {
			t.Errorf("beat %s: /status does not name it in its own middle", b.Name)
		}
	}
}

// TestHarborpulseSitesStandBehindEastQuay: the flood plain the fence is named for, and the depot
// the truck leaves from, stand behind East Quay in the manifest's own coordinates. Behind is
// inland: SOUTH of the promenade the lights stand along, the harbour water being north of it.
// East Quay is the span of its own lights.
// The console's map draws these coordinates, so a fence named for East Quay that sat anywhere else
// would show a viewer two different harbours.
func TestHarborpulseSitesStandBehindEastQuay(t *testing.T) {
	span := func(quay string) (lo, hi float64) {
		lo, hi = math.Inf(1), math.Inf(-1)
		for _, d := range harborLayout() {
			if d.kind == harborLight && d.quay == quay {
				p := harborLampPosition(d)
				lo, hi = math.Min(lo, p.Longitude), math.Max(hi, p.Longitude)
			}
		}
		return lo, hi
	}
	eastLo, eastHi := span(HarborpulseEastQuay)
	_, northHi := span(HarborpulseNorthQuay)
	bounds := func(ring [][2]float64) (minLon, maxLon, minLat, maxLat float64) {
		minLon, maxLon, minLat, maxLat = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range ring {
			minLon, maxLon = math.Min(minLon, p[0]), math.Max(maxLon, p[0])
			minLat, maxLat = math.Min(minLat, p[1]), math.Max(maxLat, p[1])
		}
		return
	}
	rings := map[string][][2]float64{}
	for _, g := range harborpulseGeoFences() {
		rings[g.Token] = g.Ring
	}
	if len(rings) != 2 || rings[HarborpulseFloodPlainFence] == nil || rings[HarborpulseDepotFence] == nil {
		t.Fatalf("fences %v; want the flood plain and the depot", rings)
	}
	pMinLon, pMaxLon, pMinLat, pMaxLat := bounds(rings[HarborpulseFloodPlainFence])
	dMinLon, dMaxLon, _, dMaxLat := bounds(rings[HarborpulseDepotFence])
	if !(pMaxLat < harborPromenadeLat) || !(dMaxLat < harborPromenadeLat) {
		t.Errorf("the plain (to lat %.4f) and the depot (to lat %.4f) must both stand inland (south) of the promenade (lat %.4f)", pMaxLat, dMaxLat, harborPromenadeLat)
	}
	if c := (pMinLon + pMaxLon) / 2; c < eastLo || c > eastHi {
		t.Errorf("the plain's centre lon %.4f is not behind East Quay's lights [%.4f, %.4f]", c, eastLo, eastHi)
	}
	if !(pMinLon > northHi) {
		t.Errorf("the plain reaches west to lon %.4f, behind North Quay (whose lights end at %.4f)", pMinLon, northHi)
	}
	if c := (dMinLon + dMaxLon) / 2; c < eastLo || c > eastHi {
		t.Errorf("the depot's centre lon %.4f is not behind East Quay's lights [%.4f, %.4f]", c, eastLo, eastHi)
	}
	if !(dMaxLat < pMinLat) {
		t.Errorf("the depot (to lat %.4f) must stand further inland than the plain (from lat %.4f)", dMaxLat, pMinLat)
	}
}

// TestHarborpulseSceneCuesAreTheTruck: the truck cues name the instants the truck's own fixes cross
// them, so a director's pre-roll lands where the platform will judge, and every fix lies on the
// route the layout declares.
func TestHarborpulseSceneCuesAreTheTruck(t *testing.T) {
	var ring [][2]float64
	for _, g := range harborpulseGeoFences() {
		if g.Token == HarborpulseFloodPlainFence {
			ring = g.Ring
		}
	}
	in := func(h float64) bool {
		f := harborTruckFix(h)
		return f.Longitude >= ring[0][0] && f.Longitude <= ring[2][0] && f.Latitude >= ring[0][1] && f.Latitude <= ring[2][1]
	}
	l := HarborpulseSceneLayout()
	c := l.Cues
	const e = 0.01
	if in(c.TruckInPlainSim-e) || !in(c.TruckInPlainSim+e) || !in(c.TruckOutPlainSim-e) || in(c.TruckOutPlainSim+e) {
		t.Fatalf("cues in %.2f out %.2f are not where the truck crosses the plain's edge", c.TruckInPlainSim, c.TruckOutPlainSim)
	}
	moving := func(h float64) bool { return *harborTruckFix(h).Speed > 0 }
	if moving(c.TruckDepartSim-e) || !moving(c.TruckDepartSim+e) || !moving(c.TruckHomeSim-e) || moving(c.TruckHomeSim+e) {
		t.Fatalf("cues depart %.2f home %.2f are not when the truck starts and stops", c.TruckDepartSim, c.TruckHomeSim)
	}
	if len(l.TruckRoute) < 2 || l.TruckRoute[0] != l.Depot {
		t.Fatalf("route %+v does not start at the depot %+v", l.TruckRoute, l.Depot)
	}
	parked := harborTruckFix((harborTruckParked + harborTruckReturn) / 2)
	if end := l.TruckRoute[len(l.TruckRoute)-1]; end != (HarborScenePoint{Lat: parked.Latitude, Lon: parked.Longitude}) {
		t.Fatalf("route ends at %+v; the truck parks at (%v, %v)", end, parked.Latitude, parked.Longitude)
	}
	for h := 0.0; h < 24; h += 0.05 {
		f := harborTruckFix(h)
		if !onRoute(l.TruckRoute, f.Latitude, f.Longitude) {
			t.Fatalf("at %.2fh the truck (%.5f, %.5f) is off the route the layout declares", h, f.Latitude, f.Longitude)
		}
	}
}

// onRoute: the point lies on a segment of the polyline, to a millionth of a degree.
func onRoute(route []HarborScenePoint, lat, lon float64) bool {
	for i := 1; i < len(route); i++ {
		a, b := route[i-1], route[i]
		dx, dy := b.Lon-a.Lon, b.Lat-a.Lat
		u := ((lon-a.Lon)*dx + (lat-a.Lat)*dy) / (dx*dx + dy*dy)
		u = math.Max(0, math.Min(1, u))
		if math.Hypot(a.Lon+u*dx-lon, a.Lat+u*dy-lat) < 1e-6 {
			return true
		}
	}
	return false
}

// TestHarborpulseControlFrameContainsEverything: the control frame strictly contains every
// position the layout carries and every fix the truck reports, so a triangulation over it covers
// them all.
func TestHarborpulseControlFrameContainsEverything(t *testing.T) {
	l := HarborpulseSceneLayout()
	f := l.ControlFrame
	if len(f) != 4 || f[0].Lat != f[1].Lat || f[2].Lat != f[3].Lat || f[0].Lon != f[3].Lon || f[1].Lon != f[2].Lon || !(f[2].Lat > f[0].Lat) || !(f[1].Lon > f[0].Lon) {
		t.Fatalf("control frame %+v is not a rectangle, south-west first and anticlockwise", f)
	}
	check := func(what string, lat, lon float64) {
		if !(lat > f[0].Lat && lat < f[2].Lat && lon > f[0].Lon && lon < f[1].Lon) {
			t.Errorf("%s (%.5f, %.5f) is not strictly inside the control frame %+v", what, lat, lon, f)
		}
	}
	for _, d := range l.Devices {
		if d.Position != nil {
			check(d.Token, d.Position.Lat, d.Position.Lon)
		}
	}
	for _, g := range l.Fences {
		for _, p := range g.Ring {
			check(g.Token, p[1], p[0])
		}
	}
	check("depot", l.Depot.Lat, l.Depot.Lon)
	for h := 0.0; h < 24; h += 0.05 {
		fx := harborTruckFix(h)
		check("truck fix", fx.Latitude, fx.Longitude)
	}
}

// TestHarborpulseStatusLayoutDigest: /status names the SHA-256 of the layout this sim generates,
// under the wire name a scene reads, and it is the digest of the committed file.
func TestHarborpulseStatusLayoutDigest(t *testing.T) {
	c := NewHarborpulse(1, Load{}).(ScenarioClockReporter).ScenarioClock(time.Now())
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// exact keys: a struct would match a missing tag's "LayoutDigest" case-insensitively
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	var wire struct{ LayoutDigest string }
	if raw, ok := keys["layoutDigest"]; !ok {
		t.Fatalf("/status has no exact \"layoutDigest\" key: %s", b)
	} else if err := json.Unmarshal(raw, &wire.LayoutDigest); err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(harborSceneFixture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.ReplaceAll(committed, []byte("\r\n"), []byte("\n")))
	if want := hex.EncodeToString(sum[:]); wire.LayoutDigest != want {
		t.Fatalf("/status layoutDigest %q; the committed layout's is %q", wire.LayoutDigest, want)
	}
	if b, _ := json.Marshal(ScenarioClock{Name: "other"}); strings.Contains(strings.ToLower(string(b)), "layoutdigest") {
		t.Fatalf("a scenario with no layout must omit layoutDigest: %s", b)
	}
}
