// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"testing"
)

// wireWidgetTypes mirrors frontend/packages/dashboards/src/types.ts's
// WIDGET_TYPES verbatim (verified against the tree) — the parser rejects any
// widget whose "type" isn't in this set.
var wireWidgetTypes = map[string]bool{
	"timeseries-chart": true,
	"latest-card":      true,
	"gauge":            true,
	"table":            true,
	"label":            true,
	"image":            true,
	"alarm-table":      true,
	"alarm-count":      true,
	"command-button":   true,
}

// heroDevices is the minimal population buildBuildingpulseDashboard needs: a hero
// device (devices[0]) carrying the "area" assignment the dashboard anchors its
// `building` context slot on.
func heroDevices() []DeviceInstance {
	return []DeviceInstance{
		{Token: "bp-therm-001", Assignments: []Assignment{{TargetType: "area", TargetToken: "bp-bldg-01"}}},
		{Token: "bp-therm-002", Assignments: []Assignment{{TargetType: "area", TargetToken: "bp-bldg-01"}}},
	}
}

// TestBuildBuildingpulseDashboardShape asserts the marshaled definition matches the
// frontend parser's hard requirements (definition.ts): a JSON object, widgets an
// array, every widget's type in WIDGET_TYPES and carrying layout.base, and — the
// ADR-039 selection amendment — a slot hierarchy where `selectedThermostat` is a
// device scoped to the `building` anchor, the data widgets bind slots, and the alarm
// table declares its drill target.
func TestBuildBuildingpulseDashboardShape(t *testing.T) {
	raw, err := buildBuildingpulseDashboard(heroDevices())
	if err != nil {
		t.Fatalf("buildBuildingpulseDashboard: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("definition is not valid JSON: %v", err)
	}
	// The dashboard-management server itself only checks "valid JSON, and the
	// top-level value is an object" (model/api.go's definitionJSON) — confirm
	// that much directly since it's the one hard backend-side gate.
	if _, ok := doc["widgets"]; !ok {
		t.Fatal("definition has no top-level widgets field")
	}

	// The canvas must carry the CSS-Grid shape the viewer's parseCanvas expects
	// (ADR-039 amendment): a grid with columns/gap/rowHeight and a sizing knob.
	canvas, ok := doc["canvas"].(map[string]any)
	if !ok {
		t.Fatalf("canvas is not a JSON object: %T", doc["canvas"])
	}
	grid, ok := canvas["grid"].(map[string]any)
	if !ok {
		t.Fatalf("canvas.grid is not a JSON object: %T", canvas["grid"])
	}
	for _, key := range []string{"columns", "gap", "rowHeight"} {
		if _, ok := grid[key]; !ok {
			t.Errorf("canvas.grid missing %q", key)
		}
	}
	if canvas["sizing"] != "fill" {
		t.Errorf("canvas.sizing = %v, want \"fill\"", canvas["sizing"])
	}

	// The slot hierarchy: `building` is a root anchor (defaultBinding to bp-bldg-01),
	// `selectedThermostat` is a device scoped to it (strategy manual) — matching
	// parseSlots/validateScopes (a scoped slot's parent must be an anchor slot).
	slots, ok := doc["slots"].(map[string]any)
	if !ok {
		t.Fatalf("definition has no slots object: %T", doc["slots"])
	}
	building, ok := slots["building"].(map[string]any)
	if !ok {
		t.Fatalf("slots.building is not an object: %T", slots["building"])
	}
	if building["type"] != "anchor" {
		t.Errorf("slots.building.type = %v, want \"anchor\"", building["type"])
	}
	bindAnchor, _ := building["defaultBinding"].(map[string]any)
	if bindAnchor["kind"] != "anchor" {
		t.Errorf("slots.building.defaultBinding.kind = %v, want \"anchor\"", bindAnchor["kind"])
	}
	anchor, _ := bindAnchor["anchor"].(map[string]any)
	if anchor["targetType"] != "area" || anchor["targetToken"] != "bp-bldg-01" {
		t.Errorf("slots.building anchor = %v, want area/bp-bldg-01", anchor)
	}
	therm, ok := slots["selectedThermostat"].(map[string]any)
	if !ok {
		t.Fatalf("slots.selectedThermostat is not an object: %T", slots["selectedThermostat"])
	}
	if therm["type"] != "device" {
		t.Errorf("slots.selectedThermostat.type = %v, want \"device\"", therm["type"])
	}
	scope, ok := therm["scope"].(map[string]any)
	if !ok {
		t.Fatalf("slots.selectedThermostat has no scope object: %T", therm["scope"])
	}
	if scope["parent"] != "building" {
		t.Errorf("selectedThermostat.scope.parent = %v, want \"building\"", scope["parent"])
	}
	if scope["strategy"] != "manual" {
		t.Errorf("selectedThermostat.scope.strategy = %v, want \"manual\"", scope["strategy"])
	}

	widgetsRaw, ok := doc["widgets"].([]any)
	if !ok {
		t.Fatalf("widgets is not a JSON array: %T", doc["widgets"])
	}
	if len(widgetsRaw) != 3 {
		t.Fatalf("expected 3 widgets (chart+table+alarm-table), got %d", len(widgetsRaw))
	}

	sawThermostatSlot := false
	sawBuildingScopedAlarmTable := false
	for i, wr := range widgetsRaw {
		widget, ok := wr.(map[string]any)
		if !ok {
			t.Fatalf("widgets[%d] is not a JSON object: %T", i, wr)
		}

		typ, ok := widget["type"].(string)
		if !ok || !wireWidgetTypes[typ] {
			t.Fatalf("widgets[%d] has type %v, not one of the 9 WIDGET_TYPES", i, widget["type"])
		}

		layout, ok := widget["layout"].(map[string]any)
		if !ok {
			t.Fatalf("widgets[%d].layout is missing or not an object", i)
		}
		base, ok := layout["base"].(map[string]any)
		if !ok {
			t.Fatalf("widgets[%d].layout has no 'base' box", i)
		}
		for _, key := range []string{"col", "colSpan", "row", "rowSpan", "z"} {
			if _, ok := base[key]; !ok {
				t.Errorf("widgets[%d].layout.base missing %q", i, key)
			}
		}

		ds, _ := widget["datasource"].(map[string]any)
		switch typ {
		case "timeseries-chart", "table":
			// Both data widgets bind the scoped device slot (not a raw device token).
			if ds["kind"] != "slot" || ds["slot"] != "selectedThermostat" {
				t.Errorf("widgets[%d] (%s) datasource = %v, want slot/selectedThermostat", i, typ, ds)
			}
			if _, ok := ds["measurements"].([]any); !ok {
				t.Errorf("widgets[%d] (%s) datasource.measurements is not an array: %T", i, typ, ds["measurements"])
			}
			sawThermostatSlot = true
		case "alarm-table":
			// The alarm table is scoped to the building and declares its drill target.
			if ds["kind"] != "slot" || ds["slot"] != "building" {
				t.Errorf("widgets[%d] (alarm-table) datasource = %v, want slot/building", i, ds)
			}
			opts, _ := widget["options"].(map[string]any)
			if opts["selectionTarget"] != "selectedThermostat" {
				t.Errorf("alarm-table options.selectionTarget = %v, want \"selectedThermostat\"", opts["selectionTarget"])
			}
			sawBuildingScopedAlarmTable = true
		}
	}

	if !sawThermostatSlot {
		t.Fatal("expected the chart + table to bind the selectedThermostat slot")
	}
	if !sawBuildingScopedAlarmTable {
		t.Fatal("expected a building-scoped alarm-table with a drill target")
	}
}

// TestBuildBuildingpulseDashboardRejectsEmptyDevices checks the guard clause:
// building a dashboard with nothing to bind to is a caller bug, not a
// producible dashboard.
func TestBuildBuildingpulseDashboardRejectsEmptyDevices(t *testing.T) {
	if _, err := buildBuildingpulseDashboard(nil); err == nil {
		t.Fatal("expected an error building a dashboard with no devices")
	}
}

// TestBuildBuildingpulseDashboardRequiresAreaAssignment checks that a hero device with
// no area assignment (a malformed scenario) surfaces as an error rather than a
// context-less dashboard.
func TestBuildBuildingpulseDashboardRequiresAreaAssignment(t *testing.T) {
	if _, err := buildBuildingpulseDashboard([]DeviceInstance{{Token: "bp-therm-001"}}); err == nil {
		t.Fatal("expected an error when the hero device has no area assignment")
	}
}
