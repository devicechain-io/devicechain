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
// slots shapes). It writes the CURRENT format natively (schemaVersion 3: square
// cells and explicit placement modes), so the frontend never has to convert a sim
// board on load. Keeping it typed here, rather than hand-strung JSON, means a
// field rename on either side shows up as a compile error or a JSON-shape test
// failure instead of a silent parser rejection at dashboard-open time.

// dashboardDefinition is the top-level DashboardDefinition shape
// (frontend/packages/dashboards/src/types.ts). schemaVersion is always
// dashboardSchemaVersion. The buildingpulse dashboard uses the ADR-039 slot model with a
// scoped context hierarchy (a `building` anchor + a `selectedThermostat` device
// scoped to it), so `slots` is populated (not nil).
type dashboardDefinition struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Title         string                   `json:"title"`
	Canvas        dashboardCanvas          `json:"canvas"`
	Widgets       []dashboardWidget        `json:"widgets"`
	Slots         map[string]dashboardSlot `json:"slots,omitempty"`
	TimeRange     *dashboardTimeRange      `json:"timeRange,omitempty"`
}

// dashboardTimeRange mirrors BoardTimeRange: the span (seconds) the board's time-aware widgets
// cover until a viewer picks another. Omitted = the frontend's own default (one hour).
type dashboardTimeRange struct {
	DefaultSeconds int `json:"defaultSeconds"`
}

type dashboardCanvas struct {
	Grid        dashboardGrid  `json:"grid"`
	Sizing      string         `json:"sizing"`
	Breakpoints map[string]int `json:"breakpoints"`
}

// dashboardGrid mirrors CanvasGrid: the authored column count, the smallest cell the board
// renders at before it reflows (px at density 1), and the gutter and padding (px at density 1;
// the frontend also accepts spacing tokens and per-axis/per-side forms, which no sim board needs).
// Cells are SQUARE: a row is as tall as the live cell is wide, so a span fixes a widget's shape
// as well as its share of the width, and the columns still stretch to fill the container.
type dashboardGrid struct {
	Columns     int `json:"columns"`
	MinCellSize int `json:"minCellSize"`
	Gutter      int `json:"gutter"`
	Padding     int `json:"padding"`
}

// commandWidgetType is the one widget type that ISSUES rather than displays — the
// only one whose presence on a board makes a demand of the scenario's devices
// (SimManifest.CommandFarEnd). Named once because both the builder that emits it
// and Validate's far-end check must mean the same widget; a typo in either would
// otherwise disagree silently, and the direction that fails open is the check.
//
// It is not gated against the TypeScript WIDGET_TYPES list here, because it does
// not need to be: a value that is not a real widget type puts an undeclared type on
// the gallery, which TestWidgetlabGalleryCarriesEveryWidgetType already fails on.
const commandWidgetType = "command-button"

// The two command controls issue as well, so a board carrying either makes the same demand of
// the scenario's devices as a command-button does.
const (
	commandSliderWidgetType = "command-slider"
	commandToggleWidgetType = "command-toggle"
)

// isCommandWidgetType reports whether a widget type issues commands. Validate's far-end check
// and the gallery tests ask this rather than comparing against one constant, so a control added
// to the list cannot be placed on a board whose devices would never answer it.
func isCommandWidgetType(widgetType string) bool {
	switch widgetType {
	case commandWidgetType, commandSliderWidgetType, commandToggleWidgetType:
		return true
	}
	return false
}

// dashboardWidget mirrors WidgetInstance. Datasource is a pointer so a widget
// that has none (a label/image) omits the field entirely rather than marshaling
// an empty object.
type dashboardWidget struct {
	Id         string                        `json:"id"`
	Type       string                        `json:"type"`
	Layout     map[string]dashboardPlacement `json:"layout"`
	Datasource *dashboardDatasource          `json:"datasource,omitempty"`
	Options    map[string]any                `json:"options,omitempty"`
}

// dashboardThreshold is one entry of the `thresholds` widget option: a severity band that
// STARTS at Value and runs to the next entry. Level is a severity NAME (normal, warning, minor,
// major, critical), never a colour -- the theme decides the colour, and the option schema
// rejects anything else. Label is optional.
type dashboardThreshold struct {
	Value float64 `json:"value"`
	Level string  `json:"level"`
	Label string  `json:"label,omitempty"`
}

// withThresholds sets a widget's `thresholds` option. The entries must ascend in Value, which
// is how the frontend reads them (it sorts, but a board should state them in the order they apply).
func withThresholds(w dashboardWidget, thresholds ...dashboardThreshold) dashboardWidget {
	w.Options["thresholds"] = thresholds
	return w
}

// dashboardPlacement is one breakpoint's GRID placement (GridPlacement in types.ts): Col/Row
// are the 0-based start cell and Cols/Rows how many cells the widget covers. Mode is always
// "grid" on the sim boards; the frontend's other mode (a widget placed relative to another) is
// not one they use. Two grid footprints may not overlap: the frontend parser refuses such a
// board, which is why no Go check for overlap exists any more.
type dashboardPlacement struct {
	Mode string `json:"mode"`
	Col  int    `json:"col"`
	Row  int    `json:"row"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
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
	// Location names the location series the selector binds, and is OMITTED when
	// unset — deliberately, and it is the opposite convention from Measurements
	// above. An empty measurements array means "every measurement", so writing it
	// explicitly makes the widening visible; an absent location means the selector
	// names NO location series, which is exactly what every definition authored
	// before the location channel carries. Emitting `"location":null` instead would
	// make a measurement widget serialize differently than it always has, and the
	// fixture gate would report the churn as a change nobody made.
	Location *dashboardLocationSelection `json:"location,omitempty"`
}

// dashboardLocationSelection mirrors the frontend's LocationSelection.
type dashboardLocationSelection struct {
	Series string `json:"series"`
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
	Candidates     *dashboardCandidates  `json:"candidates,omitempty"`
}

// dashboardCandidates mirrors SlotCandidates: a root anchor slot's picker lists only the areas of
// this area type (or the assets of this asset type).
type dashboardCandidates struct {
	AreaType  string `json:"areaType,omitempty"`
	AssetType string `json:"assetType,omitempty"`
}

// withCandidates limits an area anchor slot's picker to areas of one type.
func withCandidates(s dashboardSlot, areaType string) dashboardSlot {
	s.Candidates = &dashboardCandidates{AreaType: areaType}
	return s
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

// The definition format the builders write.
const dashboardSchemaVersion = 3

// The sim boards' canvas grid (shared by every scenario's board), matching the frontend's
// DEFAULT_GRID: 12 columns, cells of at least 56 px, an 8 px gutter and no padding. At the 960 px
// a half-screen board gets, a cell is (960 - 11*8) / 12 = ~73 px.
const (
	dashboardGridColumns     = 12
	dashboardGridMinCellSize = 56
	dashboardGridGutter      = 8
	dashboardGridPadding     = 0
)

// Slot names the buildingpulse dashboard declares. `building` is the root context (an
// area anchor); `selectedThermostat` is a device scoped to it (the per-device widgets'
// subject, and the alarm-originator drill target).
const (
	slotBuilding           = "building"
	slotSelectedThermostat = "selectedThermostat"
)

// baseLayout builds a Layout map with only the required "base" breakpoint's grid placement.
func baseLayout(col, cols, row, rows int) map[string]dashboardPlacement {
	return map[string]dashboardPlacement{"base": {Mode: "grid", Col: col, Row: row, Cols: cols, Rows: rows}}
}

// dashboardCanvasGrid is the sim boards' grid.
func dashboardCanvasGrid() dashboardGrid {
	return dashboardGrid{
		Columns:     dashboardGridColumns,
		MinCellSize: dashboardGridMinCellSize,
		Gutter:      dashboardGridGutter,
		Padding:     dashboardGridPadding,
	}
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
	buildingToken, err := deviceAreaToken(hero)
	if err != nil {
		return "", err
	}

	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         "Building Pulse",
		Canvas: dashboardCanvas{
			Grid: dashboardCanvasGrid(),
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
		// Placement on the 12-column grid of square cells: a top band of two context
		// selectors (building + thermostat, one row each), then a wide temperature chart and
		// a latest-values table side by side (7 + 5 = 12 columns, 4 rows tall), with a
		// full-width alarm table beneath (all 12 columns, 3 rows). The columns stretch, so
		// this fills the viewer at any width.
		Widgets: []dashboardWidget{
			{
				// Root context-selector: pick which building the whole dashboard shows.
				// Re-points the `building` anchor slot; the cascade fans the change out to
				// its scoped child (selectedThermostat) so every widget follows.
				Id:      "w-building-select",
				Type:    "entity-selector",
				Layout:  baseLayout(0, 6, 0, 1),
				Options: map[string]any{"title": "Building", "selectionTarget": slotBuilding},
			},
			{
				// Member selector: pick which thermostat within the selected building the
				// per-device widgets (chart + table) show — the manual companion to the
				// alarm-originator drill.
				Id:      "w-thermostat-select",
				Type:    "entity-selector",
				Layout:  baseLayout(6, 6, 0, 1),
				Options: map[string]any{"title": "Thermostat", "selectionTarget": slotSelectedThermostat},
			},
			{
				Id:     "w-chart",
				Type:   "timeseries-chart",
				Layout: baseLayout(0, 7, 1, 4),
				Datasource: &dashboardDatasource{
					Kind:         "slot",
					Slot:         slotSelectedThermostat,
					Measurements: []string{BuildingpulseTemperatureKey},
				},
				// No span of its own: the chart follows the board's time range (one hour by default), so a
				// viewer's range picker drives it.
				Options: map[string]any{"title": "Temperature"},
			},
			{
				Id:     "w-table",
				Type:   "table",
				Layout: baseLayout(7, 5, 1, 4),
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
				Layout: baseLayout(0, 12, 5, 3),
				// Scoped to the building: only this building's thermostats' alarms show
				// (the per-building context that was asked for). selectionTarget names the
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
