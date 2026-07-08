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
	Sizing      string         `json:"sizing"`
	Breakpoints map[string]int `json:"breakpoints"`
}

// dashboardGrid mirrors CanvasGrid (ADR-039 amendment 2026-07-08): a high-res fluid
// column count, a gutter in px, and a fixed row height in px. The viewer renders the
// canvas as a real CSS Grid (`repeat(columns, 1fr)`), so a definition fills whatever
// width its container gives it — no pixel widths baked in here.
type dashboardGrid struct {
	Columns   int `json:"columns"`
	Gap       int `json:"gap"`
	RowHeight int `json:"rowHeight"`
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

// dashboardBox is one breakpoint's layout box as a CSS-Grid SPAN placement (WidgetBox
// in types.ts): Col/Row are the 0-based start lines and ColSpan/RowSpan how many
// tracks the widget covers. On the buildingpulse grid (dashboardGridColumns below)
// a half-width chart is ColSpan ~14 of 24 — the columns are fluid, so the widget
// fills that fraction of whatever width the viewer has, not a fixed pixel count.
type dashboardBox struct {
	Col     int `json:"col"`
	ColSpan int `json:"colSpan"`
	Row     int `json:"row"`
	RowSpan int `json:"rowSpan"`
	Z       int `json:"z"`
}

// dashboardDatasource models only the "device" datasource kind (DeviceSelector
// in types.ts) — the only kind this scenario's widgets need. Measurements
// nil/empty means "all measurements" (table.tsx's latest-value-per-name view).
type dashboardDatasource struct {
	Kind         string   `json:"kind"`
	DeviceToken  string   `json:"deviceToken"`
	Measurements []string `json:"measurements"`
}

// The buildingpulse canvas grid: a 24-column fluid grid, 8px gutter, 40px rows —
// matching the frontend's DEFAULT_GRID. Widgets place by span across these columns.
const (
	dashboardGridColumns   = 24
	dashboardGridGap       = 8
	dashboardGridRowHeight = 40
)

// baseLayout builds a Layout map with only the required "base" breakpoint span box.
func baseLayout(col, colSpan, row, rowSpan, z int) map[string]dashboardBox {
	return map[string]dashboardBox{"base": {Col: col, ColSpan: colSpan, Row: row, RowSpan: rowSpan, Z: z}}
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
			Grid: dashboardGrid{
				Columns:   dashboardGridColumns,
				Gap:       dashboardGridGap,
				RowHeight: dashboardGridRowHeight,
			},
			// Fill the viewer's area — the fluid grid stretches to whatever width the
			// console/embed gives it (ADR-039 amendment "fill area").
			Sizing:      "fill",
			Breakpoints: map[string]int{"base": 0},
		},
		// Span placement on the 24-column grid: a wide temperature chart and a
		// latest-values table side by side across the top (14 + 10 = 24 columns, 8
		// rows tall), with a full-width alarm table beneath them (all 24 columns, 6
		// rows). The columns are fluid, so this fills the viewer at any width.
		Widgets: []dashboardWidget{
			{
				Id:     "w-chart",
				Type:   "timeseries-chart",
				Layout: baseLayout(0, 14, 0, 8, 0),
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
				Layout: baseLayout(14, 10, 0, 8, 0),
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
				Layout: baseLayout(0, 24, 8, 6, 0),
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
