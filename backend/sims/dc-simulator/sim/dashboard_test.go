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

// TestBuildBuildingpulseDashboardShape asserts the marshaled definition
// matches the frontend parser's hard requirements (definition.ts's
// parseDashboardDefinition/parseWidget/parseLayout): a JSON object,
// widgets an array, every widget's type in WIDGET_TYPES, every widget
// carrying layout.base, and the two device-bound widgets using a valid
// "device" datasource shape.
func TestBuildBuildingpulseDashboardShape(t *testing.T) {
	devices := []DeviceInstance{
		{Token: "bp-therm-001"},
		{Token: "bp-therm-002"},
	}

	raw, err := buildBuildingpulseDashboard(devices)
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

	widgetsRaw, ok := doc["widgets"].([]any)
	if !ok {
		t.Fatalf("widgets is not a JSON array: %T", doc["widgets"])
	}
	if len(widgetsRaw) != 3 {
		t.Fatalf("expected 3 widgets (chart+table+alarm-table), got %d", len(widgetsRaw))
	}

	sawDeviceDatasource := false
	sawAlarmTableWithNoDatasource := false
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

		ds, hasDatasource := widget["datasource"].(map[string]any)
		switch typ {
		case "timeseries-chart", "table":
			if !hasDatasource {
				t.Fatalf("widgets[%d] (%s) should carry a datasource", i, typ)
			}
			if ds["kind"] != "device" {
				t.Errorf("widgets[%d] (%s) datasource.kind = %v, want \"device\"", i, typ, ds["kind"])
			}
			if ds["deviceToken"] != "bp-therm-001" {
				t.Errorf("widgets[%d] (%s) datasource.deviceToken = %v, want the hero device (devices[0])", i, typ, ds["deviceToken"])
			}
			if _, ok := ds["measurements"].([]any); !ok {
				t.Errorf("widgets[%d] (%s) datasource.measurements is not an array: %T", i, typ, ds["measurements"])
			}
			sawDeviceDatasource = true
		case "alarm-table":
			if hasDatasource {
				t.Errorf("widgets[%d] (alarm-table) should have no datasource (tenant-wide)", i)
			}
			sawAlarmTableWithNoDatasource = true
		}
	}

	if !sawDeviceDatasource {
		t.Fatal("expected at least one widget with a device datasource")
	}
	if !sawAlarmTableWithNoDatasource {
		t.Fatal("expected an alarm-table widget with no datasource (tenant-wide)")
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
