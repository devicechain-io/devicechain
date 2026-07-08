// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// This file builds a dashboard-management Dashboard's opaque "definition" JSON
// document as typed Go structs, then marshals it — the only place in this
// module that must match the frontend's @devicechain/dashboards TypeScript
// parser contract exactly (schemaVersion/canvas/widgets/layout.base/datasource
// shapes). Keeping it typed here, rather than hand-strung JSON, means a field
// rename on either side shows up as a compile error or a JSON-shape test
// failure instead of a silent parser rejection at dashboard-open time.

// dashboardDefinition is the top-level DashboardDefinition shape
// (frontend/packages/dashboards/src/types.ts). schemaVersion is currently
// always 1; slots are omitted entirely (nil) since this scenario binds
// datasources directly rather than through the ADR-039 runtime slot seam.
type dashboardDefinition struct {
	SchemaVersion int               `json:"schemaVersion"`
	Title         string            `json:"title"`
	Canvas        dashboardCanvas   `json:"canvas"`
	Widgets       []dashboardWidget `json:"widgets"`
}

type dashboardCanvas struct {
	Grid        dashboardGrid  `json:"grid"`
	Breakpoints map[string]int `json:"breakpoints"`
}

type dashboardGrid struct {
	Snap bool `json:"snap"`
	Size int  `json:"size"`
}

// dashboardWidget mirrors WidgetInstance. Datasource is a pointer so a widget
// that has none (an alarm-table left tenant-wide) omits the field entirely
// rather than marshaling an empty object — the parser and hub both treat an
// absent datasource as "tenant-wide" for alarm widgets (hub.ts resolveAlarmScope).
type dashboardWidget struct {
	Id         string                  `json:"id"`
	Type       string                  `json:"type"`
	Layout     map[string]dashboardBox `json:"layout"`
	Datasource *dashboardDatasource    `json:"datasource,omitempty"`
	Options    map[string]any          `json:"options,omitempty"`
}

// dashboardBox is one breakpoint's layout box, expressed in GRID CELLS (not
// pixels): the viewer renders each widget at (x*grid.size, y*grid.size) sized
// (w*grid.size, h*grid.size) — see dashboard-renderer.tsx. With gridSize below
// that makes one cell 8px, so a readable chart is ~60-70 cells wide, NOT 12
// (12 cells = 96px = a thumbnail — the mistake a "12-column grid" intuition
// invites). The console's own editor stores the same large cell counts once a
// widget is dragged out from its 12x8 placeholder.
type dashboardBox struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
	Z int `json:"z"`
}

// dashboardDatasource models only the "device" datasource kind (DeviceSelector
// in types.ts) — the only kind this scenario's widgets need. Measurements
// nil/empty means "all measurements" (table.tsx's latest-value-per-name view).
type dashboardDatasource struct {
	Kind         string   `json:"kind"`
	DeviceToken  string   `json:"deviceToken"`
	Measurements []string `json:"measurements"`
}

// dashboardGridSize is the pixels-per-cell the definition's canvas declares;
// every box dimension below is in cells and renders at dimension*this many px.
const dashboardGridSize = 8

// baseLayout builds a Layout map with only the required "base" breakpoint box.
func baseLayout(x, y, w, h, z int) map[string]dashboardBox {
	return map[string]dashboardBox{"base": {X: x, Y: y, W: w, H: h, Z: z}}
}

// buildBuildingpulseDashboard renders the buildingpulse scenario's one
// dashboard: a temperature timeseries chart and a latest-values table both
// bound to devices[0] (the "assigned so its events carry anchors" hero
// device — bp-therm-001), plus a tenant-wide alarm table so the live MAJOR
// alarm any hot thermostat raises is always visible. devices must be non-empty
// (the manifest is malformed otherwise — Validate catches an empty population
// long before this is called).
func buildBuildingpulseDashboard(devices []DeviceInstance) (string, error) {
	if len(devices) == 0 {
		return "", fmt.Errorf("buildBuildingpulseDashboard: no devices to bind")
	}
	heroToken := devices[0].Token

	def := dashboardDefinition{
		SchemaVersion: 1,
		Title:         "Building Pulse",
		Canvas: dashboardCanvas{
			Grid:        dashboardGrid{Snap: true, Size: dashboardGridSize},
			Breakpoints: map[string]int{"base": 0},
		},
		// Boxes are in grid cells (× dashboardGridSize px). Layout: a wide
		// temperature chart (576px) and a latest-values table (384px) side by
		// side across the top row, with a full-width alarm table (960px) beneath
		// them — sized to be readable in the console viewer, not thumbnails.
		Widgets: []dashboardWidget{
			{
				Id:     "w-chart",
				Type:   "timeseries-chart",
				Layout: baseLayout(0, 0, 72, 48, 0),
				Datasource: &dashboardDatasource{
					Kind:         "device",
					DeviceToken:  heroToken,
					Measurements: []string{BuildingpulseTemperatureKey},
				},
				Options: map[string]any{"title": "Temperature", "window": 300},
			},
			{
				Id:     "w-table",
				Type:   "table",
				Layout: baseLayout(72, 0, 48, 48, 0),
				Datasource: &dashboardDatasource{
					Kind:         "device",
					DeviceToken:  heroToken,
					Measurements: []string{},
				},
				Options: map[string]any{"title": "Latest values", "precision": 1},
			},
			{
				Id:     "w-alarms",
				Type:   "alarm-table",
				Layout: baseLayout(0, 48, 120, 32, 0),
				// No Datasource: alarm-table with none is tenant-wide (every alarm
				// the viewer can see), which is what the demo wants — any of the
				// 12 thermostats raising MAJOR should show up here.
				Options: map[string]any{"title": "Alarms", "maxRows": 50},
			},
		},
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal dashboard definition: %w", err)
	}
	return string(raw), nil
}
