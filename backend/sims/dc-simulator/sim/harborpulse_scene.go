// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

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
	for _, f := range harborpulseGeoFences() {
		out.Fences = append(out.Fences, HarborSceneFence{Token: f.Token, Name: f.Name, Ring: f.Ring})
	}
	for _, b := range harborBeats {
		out.Beats = append(out.Beats, HarborSceneBeat{Name: b.name, From: b.from, To: b.to})
	}
	return out
}
