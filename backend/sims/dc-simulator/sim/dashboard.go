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
// parser contract exactly (schemaVersion/canvas/widgets/layout.base/datasource/
// slots shapes). Keeping it typed here, rather than hand-strung JSON, means a
// field rename on either side shows up as a compile error or a JSON-shape test
// failure instead of a silent parser rejection at dashboard-open time.

// dashboardDefinition is the top-level DashboardDefinition shape
// (frontend/packages/dashboards/src/types.ts). schemaVersion is currently
// always 1. The buildingpulse dashboard uses the ADR-039 slot model with a
// scoped context hierarchy (a `building` anchor + a `selectedThermostat` device
// scoped to it), so `slots` is populated (not nil).
type dashboardDefinition struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Title         string                   `json:"title"`
	Canvas        dashboardCanvas          `json:"canvas"`
	Widgets       []dashboardWidget        `json:"widgets"`
	Slots         map[string]dashboardSlot `json:"slots,omitempty"`
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
// that has none (a label/image) omits the field entirely rather than marshaling
// an empty object.
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

// dashboardDatasource models the two selector kinds this scenario needs: a "slot"
// selector (SlotSelector in types.ts) naming a slot the definition declares, and —
// unused here now that every data widget goes through a slot — a "device" selector.
// Measurements nil/empty means "all measurements" (table.tsx's latest-value view).
type dashboardDatasource struct {
	Kind         string   `json:"kind"`
	Slot         string   `json:"slot,omitempty"`
	DeviceToken  string   `json:"deviceToken,omitempty"`
	Measurements []string `json:"measurements"`
}

// dashboardSlot mirrors SlotDefinition: a named entity role widgets reference via a
// slot selector. Type is 'device'|'anchor'; DefaultBinding is the slot's own binding
// (a host manifest can override); Scope makes the slot resolve relative to a parent
// (the context hierarchy).
type dashboardSlot struct {
	Type           string                `json:"type"`
	Label          string                `json:"label,omitempty"`
	DefaultBinding *dashboardSlotBinding `json:"defaultBinding,omitempty"`
	Scope          *dashboardScope       `json:"scope,omitempty"`
}

// dashboardScope mirrors SlotScope: a scoped slot's dependency on a parent anchor
// slot. Strategy 'first' auto-binds the parent's first member; 'manual' keeps the
// current pick iff still a member (the drill target).
type dashboardScope struct {
	Parent   string `json:"parent"`
	Strategy string `json:"strategy"`
}

// dashboardSlotBinding mirrors SlotBinding: a concrete entity a slot resolves to —
// a device (by token) or an anchor target. Exactly one of DeviceToken/Anchor is set,
// per Kind.
type dashboardSlotBinding struct {
	Kind        string                 `json:"kind"`
	DeviceToken string                 `json:"deviceToken,omitempty"`
	Anchor      *dashboardAnchorTarget `json:"anchor,omitempty"`
}

// dashboardAnchorTarget mirrors AnchorTarget: a tracked relationship to a
// customer/area/asset that an anchor slot binds to.
type dashboardAnchorTarget struct {
	Relationship string `json:"relationship"`
	TargetType   string `json:"targetType"`
	TargetToken  string `json:"targetToken"`
}

// The buildingpulse canvas grid: a 24-column fluid grid, 8px gutter, 40px rows —
// matching the frontend's DEFAULT_GRID. Widgets place by span across these columns.
const (
	dashboardGridColumns   = 24
	dashboardGridGap       = 8
	dashboardGridRowHeight = 40
)

// Slot names the buildingpulse dashboard declares. `building` is the root context (an
// area anchor); `selectedThermostat` is a device scoped to it (the per-device widgets'
// subject, and the alarm-originator drill target).
const (
	slotBuilding           = "building"
	slotSelectedThermostat = "selectedThermostat"
)

// baseLayout builds a Layout map with only the required "base" breakpoint span box.
func baseLayout(col, colSpan, row, rowSpan, z int) map[string]dashboardBox {
	return map[string]dashboardBox{"base": {Col: col, ColSpan: colSpan, Row: row, RowSpan: rowSpan, Z: z}}
}

// heroAreaToken returns the area (building) token the hero device is assigned to — the
// root context the dashboard is built around. Every buildingpulse thermostat carries an
// "area" assignment (Validate enforces it), so this resolves; a device with none is a
// malformed scenario and surfaces as an error rather than a context-less dashboard.
func heroAreaToken(hero DeviceInstance) (string, error) {
	for _, a := range hero.Assignments {
		if a.TargetType == "area" {
			return a.TargetToken, nil
		}
	}
	return "", fmt.Errorf("hero device %q has no area assignment to anchor the dashboard on", hero.Token)
}

// buildBuildingpulseDashboard renders the buildingpulse scenario's one dashboard,
// wired for the ADR-039 scoped-slot context hierarchy: a `building` anchor slot
// (the root context, defaulting to the hero device's building) and a
// `selectedThermostat` device slot scoped to it. The temperature chart and
// latest-values table bind `selectedThermostat` (they show ONE thermostat within the
// building — the hero by default); the alarm table binds `building` (so it shows only
// that building's alarms) and declares `selectedThermostat` as its drill target, so
// clicking an alarm's originator device re-points the per-device widgets to it.
// devices must be non-empty (Validate catches an empty population long before this).
func buildBuildingpulseDashboard(devices []DeviceInstance) (string, error) {
	if len(devices) == 0 {
		return "", fmt.Errorf("buildBuildingpulseDashboard: no devices to bind")
	}
	hero := devices[0]
	buildingToken, err := heroAreaToken(hero)
	if err != nil {
		return "", err
	}

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
		// The context hierarchy: `building` is a root anchor (the assignment
		// relationship "assigned" from thermostats to their area), `selectedThermostat`
		// is a device scoped to it with strategy 'manual' — it defaults to the hero
		// device and follows an alarm-originator drill, kept iff still a member of the
		// selected building.
		Slots: map[string]dashboardSlot{
			slotBuilding: {
				Type:  "anchor",
				Label: "Building",
				DefaultBinding: &dashboardSlotBinding{
					Kind: "anchor",
					Anchor: &dashboardAnchorTarget{
						Relationship: assignmentRelationshipType,
						TargetType:   "area",
						TargetToken:  buildingToken,
					},
				},
			},
			slotSelectedThermostat: {
				Type:           "device",
				Label:          "Thermostat",
				DefaultBinding: &dashboardSlotBinding{Kind: "device", DeviceToken: hero.Token},
				Scope:          &dashboardScope{Parent: slotBuilding, Strategy: "manual"},
			},
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
					Kind:         "slot",
					Slot:         slotSelectedThermostat,
					Measurements: []string{BuildingpulseTemperatureKey},
				},
				Options: map[string]any{"title": "Temperature", "window": 300},
			},
			{
				Id:     "w-table",
				Type:   "table",
				Layout: baseLayout(14, 10, 0, 8, 0),
				Datasource: &dashboardDatasource{
					Kind:         "slot",
					Slot:         slotSelectedThermostat,
					Measurements: []string{},
				},
				// flashOnChange gives each value a green/red directional cue on every tick
				// (the selected thermostat is a single device, so each row is a distinct
				// measurement — the flash reads a real per-metric rise/fall).
				Options: map[string]any{"title": "Latest values", "precision": 1, "flashOnChange": true},
			},
			{
				Id:     "w-alarms",
				Type:   "alarm-table",
				Layout: baseLayout(0, 24, 8, 6, 0),
				// Scoped to the building: only this building's thermostats' alarms show
				// (the per-building context Derek asked for). selectionTarget names the
				// slot an originator drill re-points — click an alarm's device and the
				// chart + table follow it. precision rounds the triggering temperature
				// in the Value column (matching the latest-values table).
				Datasource: &dashboardDatasource{
					Kind:         "slot",
					Slot:         slotBuilding,
					Measurements: []string{},
				},
				Options: map[string]any{
					"title":           "Alarms",
					"maxRows":         50,
					"precision":       1,
					"flashOnChange":   true,
					"selectionTarget": slotSelectedThermostat,
				},
			},
		},
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal dashboard definition: %w", err)
	}
	return string(raw), nil
}
