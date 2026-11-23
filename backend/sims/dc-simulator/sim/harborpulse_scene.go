// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sync"
)

// The harborpulse scene layout: what the Unity showcase (demos/harborpulse-unity) builds the
// waterfront from. It is generated from the manifest's own constants and committed in the Unity
// project as Assets/Harborpulse/Resources/harbor_layout.json, and harborpulse_scene_test.go fails
// when the two differ, so a lamp moved, a device added or a beat retimed here cannot leave the
// scene drawing a district the scenario no longer provisions.
//
// It carries only what the manifest says. Street lights have surveyed positions (S4); gauges,
// pumps, chargers and meters have none, and the scene places them along their quay's promenade
// span, a placement the scene labels as its own rather than the platform's. Nothing here is a
// secret or a credential: tokens, external IDs, coordinates and story hours.

// HarborSceneLayoutVersion names the layout's shape. Bump it when a field changes meaning; the
// scene refuses a version it does not know.
const HarborSceneLayoutVersion = 1

// HarborSceneLayout is the committed layout file's shape.
type HarborSceneLayout struct {
	Version          int     `json:"version"`
	Scenario         string  `json:"scenario"`
	DayLengthSeconds float64 `json:"dayLengthSeconds"`
	StartHour        float64 `json:"startHour"`
	// PromenadeLat is the latitude the street lights stand along.
	PromenadeLat float64            `json:"promenadeLat"`
	Quays        []HarborSceneQuay  `json:"quays"`
	Devices      []HarborSceneDev   `json:"devices"`
	Fences       []HarborSceneFence `json:"fences"`
	Depot        HarborScenePoint   `json:"depot"`
	Beats        []HarborSceneBeat  `json:"beats"`
	Levels       HarborSceneLevels  `json:"levels"`
	// Cues are planned hints for a director: when the scenario schedules each scripted event,
	// and the wall-time durations its rules wait. No caption may depend on them; the platform's
	// record is the evidence.
	Cues HarborSceneCues `json:"cues"`
	// TruckRoute is the route the truck drives, depot first, as waypoints.
	TruckRoute []HarborScenePoint `json:"truckRoute"`
	// ControlFrame is a rectangle, south-west corner first and anticlockwise, that strictly
	// contains every position in this file. With the fence and depot rings' corners and the
	// route's waypoints it is the set of manifest-side control points a presentation map
	// triangulates.
	ControlFrame []HarborScenePoint `json:"controlFrame"`
}

// HarborSceneCues are the scenario's planned cues. Keys carry their unit: Sim is a sim hour
// (from the run's first midnight, so day 1 at 02:00 is 26), S is wall seconds.
type HarborSceneCues struct {
	TruckDepartSim     float64 `json:"truckDepartSim"`
	TruckInPlainSim    float64 `json:"truckInPlainSim"`
	TruckOutPlainSim   float64 `json:"truckOutPlainSim"`
	TruckHomeSim       float64 `json:"truckHomeSim"`
	ReplacementSim     float64 `json:"replacementSim"`
	RolloutSim         float64 `json:"rolloutSim"`
	LampServiceFromSim float64 `json:"lampServiceFromSim"`
	LampServiceToSim   float64 `json:"lampServiceToSim"`
	AirLockFromSim     float64 `json:"airLockFromSim"`
	AirLockToSim       float64 `json:"airLockToSim"`
	SilenceS           float64 `json:"silenceS"`
	AirLockHoldS       float64 `json:"airLockHoldS"`
	EscalateAfterS     float64 `json:"escalateAfterS"`
}

// HarborSceneQuay is one quay (a platform area).
type HarborSceneQuay struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// HarborScenePoint is a WGS84 position.
type HarborScenePoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// HarborSceneDev is one device the scene draws. Position is the manifest's surveyed position,
// absent for every device the manifest gives none.
type HarborSceneDev struct {
	Token      string            `json:"token"`
	ExternalId string            `json:"externalId"`
	DeviceType string            `json:"deviceType"`
	Kind       string            `json:"kind"`
	Quay       string            `json:"quay"`
	Index      int               `json:"index"`
	Position   *HarborScenePoint `json:"position,omitempty"`
}

// HarborSceneFence is a geofence ring, [lon, lat] pairs as the manifest declares it.
type HarborSceneFence struct {
	Token string       `json:"token"`
	Name  string       `json:"name"`
	Ring  [][2]float64 `json:"ring"`
}

// HarborSceneBeat is one story beat and the sim hours it spans.
type HarborSceneBeat struct {
	Name string  `json:"name"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// HarborSceneLevels are the water levels the scene marks on its gauge boards.
type HarborSceneLevels struct {
	FloodMinorM    float64 `json:"floodMinorM"`
	FloodMajorM    float64 `json:"floodMajorM"`
	FloodCriticalM float64 `json:"floodCriticalM"`
	QuayFloodingM  float64 `json:"quayFloodingM"`
	WetWellHighM   float64 `json:"wetWellHighM"`
}

// The scene's names for the kinds of device, which are the scene's own vocabulary.
const (
	harborSceneGauge       = "gauge"
	harborSceneLoraGauge   = "lora-gauge"
	harborSceneLegacyGauge = "legacy-gauge"
	harborScenePump        = "pump"
	harborSceneLight       = "light"
	harborSceneCharger     = "charger"
	harborSceneMeter       = "meter"
	harborSceneTruck       = "truck"
	// The S6 populations (harborpulse_s6_scene.go): a charger that holds an MQTT session under a
	// token, one that holds it under a client certificate, and a lamp that asks for its commands.
	harborSceneMQTTCharger = "mqtt-charger"
	harborSceneCertCharger = "cert-charger"
	harborScenePullLight   = "pull-light"
)

func harborSceneKind(d harborDevice) string {
	switch d.kind {
	case harborGauge:
		return harborSceneGauge
	case harborLoraGauge:
		if d.deviceType == HarborpulseLegacyGaugeType {
			return harborSceneLegacyGauge
		}
		return harborSceneLoraGauge
	case harborPump:
		return harborScenePump
	case harborLight:
		return harborSceneLight
	case harborCharger:
		return harborSceneCharger
	case harborMeter:
		return harborSceneMeter
	case harborTruck:
		return harborSceneTruck
	}
	panic("harborSceneKind: a harbour device kind with no scene name")
}

// HarborpulseSceneLayout is the layout of the scenario as authored: the default day length and no
// defects, which is the district the scene draws.
func HarborpulseSceneLayout() HarborSceneLayout {
	out := HarborSceneLayout{
		Version:          HarborSceneLayoutVersion,
		Scenario:         "harborpulse",
		DayLengthSeconds: harborpulseDayLength.Seconds(),
		StartHour:        harborpulseStartHour,
		PromenadeLat:     harborPromenadeLat,
		Depot:            HarborScenePoint{Lat: harborDepotLat, Lon: harborDepotLon},
		Levels: HarborSceneLevels{
			FloodMinorM: HarborpulseFloodMinorM, FloodMajorM: HarborpulseFloodMajorM, FloodCriticalM: HarborpulseFloodCriticalM,
			QuayFloodingM: HarborpulseQuayFloodingM, WetWellHighM: HarborpulseWetWellM,
		},
	}
	for _, q := range harborQuays {
		name := q.name
		switch q.token {
		case HarborpulseNorthQuay:
			name = "North Quay"
		case HarborpulseEastQuay:
			name = "East Quay"
		case HarborpulseBasin:
			name = "The Basin"
		}
		out.Quays = append(out.Quays, HarborSceneQuay{Token: q.token, Name: name})
	}
	for _, d := range harborLayout() {
		dev := HarborSceneDev{
			Token: d.token, ExternalId: d.externalId, DeviceType: d.deviceType,
			Kind: harborSceneKind(d), Quay: d.quay, Index: d.index,
		}
		if d.kind == harborLight {
			p := harborLampPosition(d)
			dev.Position = &HarborScenePoint{Lat: p.Latitude, Lon: p.Longitude}
		}
		out.Devices = append(out.Devices, dev)
	}
	out.Devices = append(out.Devices, harborS6SceneDevices()...)
	for _, f := range harborpulseGeoFences() {
		out.Fences = append(out.Fences, HarborSceneFence{Token: f.Token, Name: f.Name, Ring: f.Ring})
	}
	for _, b := range harborBeats {
		out.Beats = append(out.Beats, HarborSceneBeat{Name: b.name, From: b.from, To: b.to})
	}
	out.Cues = HarborSceneCues{
		TruckDepartSim: harborTruckDepart, TruckInPlainSim: HarborpulseTruckInPlainFrom,
		TruckOutPlainSim: HarborpulseTruckInPlainTo, TruckHomeSim: harborTruckHome,
		ReplacementSim: HarborpulseReplacementHour, RolloutSim: HarborpulseRolloutHour,
		LampServiceFromSim: HarborpulseLampServiceFrom, LampServiceToSim: HarborpulseLampServiceTo,
		AirLockFromSim: harborAirLockFrom, AirLockToSim: harborAirLockTo,
		SilenceS: HarborpulseSilence.Seconds(), AirLockHoldS: HarborpulseAirLockHold.Seconds(),
		EscalateAfterS: HarborpulseEscalateAfter.Seconds(),
	}
	for _, p := range harborTruckRoute() {
		out.TruckRoute = append(out.TruckRoute, HarborScenePoint{Lat: p[1], Lon: p[0]})
	}
	out.ControlFrame = harborControlFrame(out)
	return out
}

// harborControlFrameMargin is how far, in degrees, the control frame stands outside the outermost
// position, before it is rounded outward to a thousandth of a degree.
const harborControlFrameMargin = 0.005

// harborControlFrame is the rectangle around every position the layout carries: the surveyed
// lights, the fence rings, the depot and the truck route.
func harborControlFrame(l HarborSceneLayout) []HarborScenePoint {
	minLat, maxLat, minLon, maxLon := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	grow := func(lat, lon float64) {
		minLat, maxLat = math.Min(minLat, lat), math.Max(maxLat, lat)
		minLon, maxLon = math.Min(minLon, lon), math.Max(maxLon, lon)
	}
	for _, d := range l.Devices {
		if d.Position != nil {
			grow(d.Position.Lat, d.Position.Lon)
		}
	}
	for _, f := range l.Fences {
		for _, p := range f.Ring {
			grow(p[1], p[0])
		}
	}
	grow(l.Depot.Lat, l.Depot.Lon)
	for _, p := range l.TruckRoute {
		grow(p.Lat, p.Lon)
	}
	down := func(v float64) float64 { return math.Floor((v-harborControlFrameMargin)*1000) / 1000 }
	up := func(v float64) float64 { return math.Ceil((v+harborControlFrameMargin)*1000) / 1000 }
	s, n, w, e := down(minLat), up(maxLat), down(minLon), up(maxLon)
	return []HarborScenePoint{{Lat: s, Lon: w}, {Lat: s, Lon: e}, {Lat: n, Lon: e}, {Lat: n, Lon: w}}
}

// HarborpulseSceneLayoutJSON is the layout file's exact bytes: indented JSON and a final newline.
func HarborpulseSceneLayoutJSON() ([]byte, error) {
	b, err := json.MarshalIndent(HarborpulseSceneLayout(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var harborLayoutDigest = sync.OnceValue(func() string {
	b, err := HarborpulseSceneLayoutJSON()
	if err != nil {
		panic("harborpulse: the scene layout does not marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
})

// HarborpulseLayoutDigest is the lowercase hex SHA-256 of the layout file this sim generates. A
// scene compares it with the file it was built with, line endings normalised to LF, and turns its
// director off when they differ.
func HarborpulseLayoutDigest() string { return harborLayoutDigest() }
