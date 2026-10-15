// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sitepulseDevices(t *testing.T) []DeviceInstance {
	t.Helper()
	m := NewSitepulse(1, Load{}).Manifest()
	return m.Expand(m.Seed)
}

func newPlane(t *testing.T) *sitepulseDevicePlane {
	t.Helper()
	p, err := newSitepulseDevicePlane(sitepulseDevices(t))
	if err != nil {
		t.Fatalf("newSitepulseDevicePlane: %v", err)
	}
	return p
}

// The seeded values must be the scene's, or SP-HL-0003 starts with a different tank here
// than in the player. The expected numbers come from an independent implementation of
// StableRandom (unsigned 64-bit arithmetic in Python), not from this code.
func TestStableRandomMatchesTheScene(t *testing.T) {
	cases := []struct {
		id         string
		seed       uint64
		unit1      float64
		unit7      float64
		unitStream uint64
	}{
		{"SP-HL-0003", 16449483404109220401, 0.09223735705907354, 0.37352811363191263, 7},
		{"SP-DZ-0002", 4306765437877570800, 0.7689915638629728, 0.1193926150799054, 7},
	}
	for _, c := range cases {
		if got := stableSeed(c.id); got != c.seed {
			t.Errorf("stableSeed(%q) = %d, want %d", c.id, got, c.seed)
		}
		if got := stableUnit(c.seed, 1); got != c.unit1 {
			t.Errorf("stableUnit(%q, 1) = %v, want %v", c.id, got, c.unit1)
		}
		if got := stableUnit(c.seed, c.unitStream); got != c.unit7 {
			t.Errorf("stableUnit(%q, 7) = %v, want %v", c.id, got, c.unit7)
		}
	}
	// And the model starts where the scene's does: fuel = 35 + 60 * Unit(seed, 1).
	m := newSitepulseMachine(sitepulseHauler, "SP-HL-0003")
	if want := 35.0 + 60.0*0.09223735705907354; m.fuel != want {
		t.Errorf("SP-HL-0003 starts at %v %% fuel, want %v", m.fuel, want)
	}
}

// The embedded fleet is a COPY of the scene's generated tracks. A regenerated fleet that
// is not copied here would drive the machines on routes the scene no longer draws.
func TestFleetTracksAreTheScenes(t *testing.T) {
	src := filepath.Join("..", "..", "..", "..", "demos", "sitepulse-unity", "Assets", "Sitepulse", "Data", "quarry_fleet_live.json")
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read the scene's fleet tracks: %v", err)
	}
	if !bytes.Equal(want, sitepulseFleetTracks) {
		t.Fatalf("sim/sitepulse_fleet_tracks.json differs from %s: copy it again", src)
	}
}

type reading struct {
	m   map[string]float64
	fix *Fix
}

func readingsAt(p *sitepulseDevicePlane) map[string]reading {
	out := map[string]reading{}
	for i, r := range p.readings() {
		out[p.units[i].device.ExternalId] = reading{r.Measurements, r.Fix}
	}
	return out
}

// The readings at a moment of the run are a function of that moment, not of how the
// ticks that reached it were spaced.
func TestReadingsDoNotDependOnTickSpacing(t *testing.T) {
	a, b := newPlane(t), newPlane(t)
	for s := 1.0; s <= 1800; s += 3 {
		a.advanceTo(s)
	}
	a.advanceTo(1800)
	for s := 7.3; s <= 1800; s += 11.9 {
		b.advanceTo(s)
	}
	b.advanceTo(1800)
	ra, rb := readingsAt(a), readingsAt(b)
	for id, x := range ra {
		y := rb[id]
		for k, v := range x.m {
			if y.m[k] != v {
				t.Errorf("%s %s: %v after 3 s ticks, %v after 11.9 s ticks", id, k, v, y.m[k])
			}
		}
		if (x.fix == nil) != (y.fix == nil) || (x.fix != nil && (x.fix.Latitude != y.fix.Latitude || x.fix.Longitude != y.fix.Longitude)) {
			t.Errorf("%s: positions differ between tick spacings", id)
		}
	}
}

func TestEachKindReportsItsOwnKeys(t *testing.T) {
	p := newPlane(t)
	p.advanceTo(30)
	keys := func(m map[string]float64) string {
		var out []string
		for _, k := range []string{SitepulseFuelKey, SitepulseEngineTempKey, SitepulseEngineHoursKey, SitepulsePayloadKey, SitepulseTyreKey, SitepulseThroughputKey, SitepulsePlantRunningKey} {
			if _, ok := m[k]; ok {
				out = append(out, k)
			}
		}
		return strings.Join(out, ",")
	}
	want := map[string]string{
		"SP-HL-0001": "fuel_pct,engine_temp_c,engine_hours,payload_t,tyre_pressure_kpa",
		"SP-LD-0001": "fuel_pct,engine_temp_c,engine_hours,payload_t,tyre_pressure_kpa",
		"SP-DZ-0001": "fuel_pct,engine_temp_c,engine_hours",
		"SP-PL-0001": "throughput_tph,plant_running",
	}
	r := readingsAt(p)
	for id, k := range want {
		if got := keys(r[id].m); got != k {
			t.Errorf("%s reports %s, want %s", id, got, k)
		}
	}
	if r["SP-PL-0001"].fix != nil {
		t.Error("the plant reported a position; it stands still and has no track")
	}
	if r["SP-DZ-0001"].fix == nil {
		t.Error("a dozer reported no position")
	}
}

// The script crosses each of the platform's three rules on the machine it names, and NO
// other machine crosses any rule in an hour and a quarter: an alarm on the board is one
// the script raised, never the model drifting.
func TestScriptCrossesEachRuleAndNothingElseDoes(t *testing.T) {
	p := newPlane(t)
	type crossing struct{ first float64 }
	crossed := map[string]*crossing{}
	note := func(key string, at float64) {
		if crossed[key] == nil {
			crossed[key] = &crossing{first: at}
		}
	}
	fuelMachines := map[string]bool{}
	for _, id := range sitepulseFuelRotation {
		fuelMachines[id] = true
	}
	const end = 75 * 60
	lowFuelSpans := map[string]float64{}
	for s := 5.0; s <= end; s += 5 {
		p.advanceTo(s)
		for id, r := range readingsAt(p) {
			if v, ok := r.m[SitepulseFuelKey]; ok && v < SitepulseLowFuelThreshold {
				note(id+"|fuel", s)
				lowFuelSpans[id] += 5
				if !fuelMachines[id] {
					t.Fatalf("%s ran out of fuel at %v s without being in the low-fuel rotation", id, s)
				}
			}
			if v, ok := r.m[SitepulseEngineTempKey]; ok && v > SitepulseOverheatThreshold {
				note(id+"|temp", s)
				if id != sitepulseOverheatingMachine {
					t.Fatalf("%s overheated (%v C) at %v s", id, v, s)
				}
			}
			if v, ok := r.m[SitepulseTyreKey]; ok && v < SitepulseTyrePressureThreshold {
				note(id+"|tyre", s)
				if id != sitepulseLeakingMachine {
					t.Fatalf("%s lost tyre pressure (%v kPa) at %v s", id, v, s)
				}
			}
			if v, ok := r.m[SitepulseThroughputKey]; ok && (v < 700 || v > 900) {
				t.Fatalf("the plant's throughput %v t/h at %v s is outside 700..900", v, s)
			}
		}
	}
	if c := crossed[sitepulseOverheatingMachine+"|temp"]; c == nil || c.first > 300 {
		t.Errorf("the overheating machine did not cross %v C within 5 minutes: %+v", SitepulseOverheatThreshold, c)
	}
	if c := crossed[sitepulseLeakingMachine+"|tyre"]; c == nil || c.first < sitepulseTyreLeakAtSeconds || c.first > sitepulseTyreLeakAtSeconds+sitepulseTyreCrossSeconds+30 {
		t.Errorf("the leaking machine did not cross %v kPa within its leak's window: %+v", SitepulseTyrePressureThreshold, c)
	}
	// Five episodes start in 75 minutes (at 1, 16, 31, 46 and 61 minutes); each one goes
	// below the line within its minute and a half and stays there until it is refuelled.
	for k, id := range sitepulseFuelRotation[:5] {
		start := sitepulseFuelFirstSeconds + float64(k)*sitepulseFuelCycleSeconds
		c := crossed[id+"|fuel"]
		if c == nil || c.first < start || c.first > start+sitepulseFuelCrossSeconds+30 {
			t.Errorf("episode %d: %s did not cross the low-fuel line soon after %v s: %+v", k, id, start, c)
		}
		if span := lowFuelSpans[id]; span < sitepulseFuelLowSeconds-sitepulseFuelCrossSeconds-30 || span > sitepulseFuelLowSeconds {
			t.Errorf("episode %d: %s sat below the line for %v s", k, id, span)
		}
	}
	// And the first one was refuelled.
	if v := readingsAt(p)[sitepulseFuelRotation[0]].m[SitepulseFuelKey]; v < sitepulseRefuelLowestPct-2 {
		t.Errorf("%s was not refuelled: %v %%", sitepulseFuelRotation[0], v)
	}
}

// Machines move on the site, haul trucks carry 86..94 t when loaded, and every fix is a
// position the platform's decoder accepts.
func TestMachinesDriveTheSite(t *testing.T) {
	p := newPlane(t)
	moved := map[string]bool{}
	first := map[string][2]float64{}
	loads := 0
	for s := 3.0; s <= 600; s += 3 {
		p.advanceTo(s)
		for id, r := range readingsAt(p) {
			if r.fix == nil {
				continue
			}
			f := r.fix
			if math.Abs(f.Latitude-sitepulseOriginLatitude) > 0.02 || math.Abs(f.Longitude-sitepulseOriginLongitude) > 0.03 {
				t.Fatalf("%s at %v,%v is off the site", id, f.Latitude, f.Longitude)
			}
			if *f.Heading < 0 || *f.Heading >= 360 || *f.Speed < 0 || *f.Speed > 25 {
				t.Fatalf("%s: heading %v, speed %v", id, *f.Heading, *f.Speed)
			}
			if p0, ok := first[id]; !ok {
				first[id] = [2]float64{f.Latitude, f.Longitude}
			} else if p0 != [2]float64{f.Latitude, f.Longitude} {
				moved[id] = true
			}
			if pl, ok := r.m[SitepulsePayloadKey]; ok && strings.HasPrefix(id, "SP-HL-") && pl > 0 {
				loads++
				if pl < 86 || pl > 94 {
					t.Fatalf("%s carries %v t", id, pl)
				}
			}
		}
	}
	if len(moved) != 18 {
		t.Errorf("%d of 18 machines moved in ten minutes", len(moved))
	}
	if loads == 0 {
		t.Error("no haul truck was ever loaded")
	}
}

func TestADeviceWithNoTrackIsRefused(t *testing.T) {
	devices := append(sitepulseDevices(t), DeviceInstance{Token: "sp-hauler-07", ExternalId: "SP-HL-0007"})
	if _, err := newSitepulseDevicePlane(devices); err == nil || !strings.Contains(err.Error(), "SP-HL-0007") {
		t.Fatalf("a hauler with no track was not refused by name: %v", err)
	}
	if _, err := newSitepulseDevicePlane([]DeviceInstance{{Token: "x", ExternalId: "BP-0001"}}); err == nil {
		t.Fatal("a device that is not a sitepulse machine was not refused")
	}
}

func TestPlayDevicesIsForSitepulseOnly(t *testing.T) {
	if err := PlayDevices(NewDevicepulse(1, Load{}), nil); err == nil {
		t.Fatal("PlayDevices accepted devicepulse")
	}
	s := NewSitepulse(1, Load{})
	if err := PlayDevices(s, nil); err != nil {
		t.Fatalf("PlayDevices(sitepulse): %v", err)
	}
}

// Without PlayDevices the scenario's Tick still publishes nothing: the Unity player is
// the device plane.
func TestTickPublishesNothingUnlessPlaying(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	rt := &Runtime{HTTPClient: srv.Client(), Devices: sitepulseDevices(t)}
	rt.Endpoints.Ingress = srv.URL
	if err := NewSitepulse(1, Load{}).Tick(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("a sitepulse Tick without --play-devices posted %d events", posts)
	}
}

// Playing, a tick posts one measurement per device and one location per machine through
// the real ingress route, and the publish log holds exactly what was accepted.
func TestPlayingPublishesAndLogsWhatWasAccepted(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var ev struct {
			Device    string `json:"device"`
			EventType string `json:"eventType"`
		}
		_ = json.Unmarshal(raw, &ev)
		mu.Lock()
		bodies[ev.EventType]++
		mu.Unlock()
		// Refuse the plant, so the log can be seen to hold only accepted events.
		if ev.Device == "sp-plant-01" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := NewSitepulse(1, Load{})
	var log bytes.Buffer
	if err := PlayDevices(s, &log); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.(*sitepulse).player.now = func() time.Time { return clock }
	rt := &Runtime{HTTPClient: srv.Client(), Devices: sitepulseDevices(t)}
	rt.Endpoints.Ingress = srv.URL
	if err := s.Tick(context.Background(), rt); err == nil {
		t.Fatal("a refused plant reading did not fail the tick")
	}
	if bodies["Measurement"] != 19 || bodies["Location"] != 18 {
		t.Fatalf("posted %v, want 19 measurements and 18 locations", bodies)
	}
	var lines []PublishLogEntry
	for _, l := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var e PublishLogEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("publish log line %q: %v", l, err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 18+18 {
		t.Fatalf("the publish log holds %d lines, want the 36 accepted events", len(lines))
	}
	for _, e := range lines {
		if e.Device == "sp-plant-01" {
			t.Fatal("the publish log holds the plant's REFUSED reading")
		}
		if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil || len(e.Values) == 0 {
			t.Fatalf("publish log entry %+v", e)
		}
	}
}
