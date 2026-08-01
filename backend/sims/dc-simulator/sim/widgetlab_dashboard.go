// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// The widgetlab boards: one instance of EVERY widget type on a gallery board, and a
// curated set on a stress board bound to the pathological devices.
//
// Two boards because a showcase and an exerciser want opposite things and one board
// cannot be both. The gallery is a CATALOG — someone about to author a dashboard
// looks at it to see what is available — so it is laid out to be read, with nominal
// data. The stress board is maintainer-facing and deliberately ugly.
//
// Coverage is asserted on the GALLERY only. Several widget types have no
// pathological axis expressible as a board placement: a command-button's pathology
// is that the ack never arrives and an entity-selector's is that its candidate set
// is empty — both live in device behaviour, not in a second layout box. Forcing all
// ten onto the stress board would accrete placements that are nominal in all but
// location, diluting the exact signal that board exists to give.
//
// Every builder takes the same shape — (id, box, subject) → dashboardWidget — so
// adding a widget type is one function and one line in the board, and so the layout
// is visibly separate from the binding.

// Slots the widgetlab boards declare. `zone` is the root context (an area anchor);
// `sensor` is a device scoped to it, and `edge` is the pathological device the
// stress board binds directly.
const (
	widgetlabSlotZone   = "zone"
	widgetlabSlotSensor = "sensor"
	widgetlabSlotEdge   = "edge"
)

// widgetSubject is what a builder binds to: a slot name plus the measurements it
// selects. An empty Slot means the widget carries no datasource at all (label,
// image, entity-selector), which is a different thing from a slot selecting no
// measurements (the table's "every measurement" view).
type widgetSubject struct {
	Slot         string
	Measurements []string
}

// noSubject marks a widget that binds nothing.
var noSubject = widgetSubject{}

func (s widgetSubject) datasource() *dashboardDatasource {
	if s.Slot == "" {
		return nil
	}
	// Measurements is deliberately non-nil even when empty, but NOT because a null
	// would be rejected — the parser coerces a non-array to [] (stringArrayAt) and
	// keeps the datasource. That coercion IS the hazard: [] means "every measurement
	// this subject reports", so a nil list silently WIDENS the subscription rather
	// than narrowing it, and a chart that meant to draw two series draws every metric
	// the device has. Writing the array explicitly makes the empty case a choice a
	// reader can see rather than a value that arrived by omission.
	measurements := s.Measurements
	if measurements == nil {
		measurements = []string{}
	}
	return &dashboardDatasource{Kind: "slot", Slot: s.Slot, Measurements: measurements}
}

// box is a widget's placement, named so a board reads as a layout rather than as
// five positional integers per line.
type box struct{ col, colSpan, row, rowSpan int }

func (b box) layout() map[string]dashboardBox {
	return baseLayout(b.col, b.colSpan, b.row, b.rowSpan, 0)
}

// ---- One builder per widget type ---------------------------------------------
//
// Each names only the options its widget actually reads. The option KEYS are the
// contract with @devicechain/widgets, which Go cannot check — that is what the
// fixture gate is for. What these do give is one place per type where the shape is
// stated, so a new widget is an addition rather than an edit to a wall of literals.

func buildTimeSeriesChartWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "timeseries-chart", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{"title": title, "window": widgetlabChartWindow},
	}
}

func buildLatestCardWidget(id string, b box, s widgetSubject, title, measurement, unit string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "latest-card", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit,
			"precision": 1, "flashOnChange": true,
		},
	}
}

// The gauge's scale is the sweep's own bounds, so the needle visits both ends of it
// rather than sitting in a narrow band of an arbitrary 0..100 scale.
func buildGaugeWidget(id string, b box, s widgetSubject, title, measurement, unit string, min, max float64) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "gauge", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit,
			"min": min, "max": max, "flashOnChange": true,
		},
	}
}

func buildTableWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "table", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{"title": title, "precision": 2, "flashOnChange": true},
	}
}

func buildLabelWidget(id string, b box, text, align string, fontSize int) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "label", Layout: b.layout(),
		Options: map[string]any{"text": text, "align": align, "fontSize": fontSize},
	}
}

// The image URL is a data: URI, not a link to anything. A board that reached out to
// the network would fail closed behind an air-gapped deploy's egress policy and
// render the widget's empty state — which reads as a broken widget rather than a
// blocked request, on the one board whose job is to show what a widget looks like.
func buildImageWidget(id string, b box, title, url, alt, fit string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "image", Layout: b.layout(),
		Options: map[string]any{"title": title, "url": url, "alt": alt, "fit": fit},
	}
}

// No `state` filter, deliberately. Filtering to ACTIVE makes a resolve render as a
// vanishing row, so the cleared and acknowledged states — half of what this widget
// draws, and the half the sweep is designed to produce — would never appear on the
// board whose job is showing what the widget looks like.
func buildAlarmTableWidget(id string, b box, s widgetSubject, title, drillTo string) dashboardWidget {
	options := map[string]any{
		"title": title, "maxRows": 25,
		"precision": 1, "flashOnChange": true,
	}
	if drillTo != "" {
		options["selectionTarget"] = drillTo
	}
	return dashboardWidget{
		Id: id, Type: "alarm-table", Layout: b.layout(), Datasource: s.datasource(), Options: options,
	}
}

// The severity filter is the WIRE form — the tier as it lands on the durable alarm
// row, which is the authoring severity uppercased. Filtering on the lowercase
// authoring form would match nothing and render a permanent zero that looks like a
// quiet system.
func buildAlarmCountWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "alarm-count", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "state": "ACTIVE", "severity": WidgetlabAlarmSeverityWire,
		},
	}
}

// commandName and parameterSchema are the SAME constants the profile's command
// definition is authored from. A button naming a command the published vocabulary
// does not contain renders a form, sends, and fails at the delivery boundary.
func buildCommandButtonWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "command-button", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "commandName": WidgetlabCommandKey, "commandLabel": "Set Setpoint",
			"parameterSchema": widgetlabParameterSchema, "maxRows": 10,
		},
	}
}

func buildEntitySelectorWidget(id string, b box, title, target string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "entity-selector", Layout: b.layout(),
		Options: map[string]any{"title": title, "selectionTarget": target},
	}
}

// ---- Shared board scaffolding -------------------------------------------------

// widgetlabChartWindow is the rolling sample buffer the charts keep.
//
// useMeasurementStream trims that buffer GLOBALLY, not per measurement, so a chart
// drawing N series retains window/N samples of each. Sized here for the chart that
// draws two, leaving one full sweep per series — enough to watch the curve turn
// around rather than a rising line whose shape a viewer has to take on trust. A
// third series would cut each to two thirds of a period.
const widgetlabChartWindow = 2 * WidgetlabSweepTicks

// A 1x1 transparent PNG. Inline for the reason buildImageWidget explains, and the
// smallest thing that proves the widget renders an image rather than its empty state.
const widgetlabImageDataURI = "data:image/png;base64," +
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func widgetlabCanvas() dashboardCanvas {
	return dashboardCanvas{
		Grid:        dashboardGrid{Columns: dashboardGridColumns, Gap: dashboardGridGap, RowHeight: dashboardGridRowHeight},
		Sizing:      "fill",
		Breakpoints: map[string]int{"base": 0},
	}
}

// deviceAreaToken returns the area a device is assigned to. Every widgetlab device
// carries one (Validate enforces it), so a device without one is a malformed
// scenario and surfaces as an error rather than a context-less board.
func deviceAreaToken(d DeviceInstance) (string, error) {
	for _, a := range d.Assignments {
		if a.TargetType == "area" {
			return a.TargetToken, nil
		}
	}
	return "", fmt.Errorf("device %q has no area assignment to anchor a board on", d.Token)
}

// firstOfType returns the first device of a given type, which is how each board
// picks its subject. It returns an error rather than a zero device: binding a board
// to an empty token produces a widget that renders "device unavailable", which is
// indistinguishable from a deleted device.
func firstOfType(devices []DeviceInstance, deviceType string) (DeviceInstance, error) {
	for _, d := range devices {
		if d.DeviceTypeToken == deviceType {
			return d, nil
		}
	}
	return DeviceInstance{}, fmt.Errorf("no device of type %q to bind a board to", deviceType)
}

// ---- The gallery board --------------------------------------------------------

// buildWidgetlabGallery renders one instance of every widget type over the nominal
// sensors, laid out to be read: a title band, then the single-value widgets, the
// chart and table, the alarm pair, and the control/selection row.
//
// Coverage over WIDGET_TYPES is asserted on the TypeScript side, which is the only
// side that knows the full list. What this file guarantees is that each type it does
// place is placed once, with a real subject.
func buildWidgetlabGallery(devices []DeviceInstance) (string, error) {
	sensor, err := firstOfType(devices, WidgetlabDeviceTypeToken)
	if err != nil {
		return "", err
	}
	zoneToken, err := deviceAreaToken(sensor)
	if err != nil {
		return "", err
	}

	onSensor := func(measurements ...string) widgetSubject {
		return widgetSubject{Slot: widgetlabSlotSensor, Measurements: measurements}
	}
	onZone := widgetSubject{Slot: widgetlabSlotZone, Measurements: []string{}}

	def := dashboardDefinition{
		SchemaVersion: 1,
		Title:         "Widget Lab",
		Canvas:        widgetlabCanvas(),
		Slots: map[string]dashboardSlot{
			widgetlabSlotZone: {
				Type:  "anchor",
				Label: "Zone",
				DefaultBinding: &dashboardSlotBinding{
					Kind: "anchor",
					Anchor: &dashboardAnchorTarget{
						Relationship: assignmentRelationshipType,
						TargetType:   "area",
						TargetToken:  zoneToken,
					},
				},
			},
			widgetlabSlotSensor: {
				Type:           "device",
				Label:          "Sensor",
				DefaultBinding: &dashboardSlotBinding{Kind: "device", DeviceToken: sensor.Token},
				// 'manual' keeps the viewer's pick as long as it is still a member of
				// the selected zone, which is what makes the two selectors compose.
				Scope: &dashboardScope{Parent: widgetlabSlotZone, Strategy: "manual"},
			},
		},
		Widgets: []dashboardWidget{
			buildLabelWidget("wl-title", box{0, 24, 0, 2},
				"Widget Lab — one of every dashboard widget", "left", 20),

			buildLatestCardWidget("wl-card", box{0, 6, 2, 4}, onSensor(WidgetlabTemperatureKey),
				"Temperature", WidgetlabTemperatureKey, "C"),
			buildGaugeWidget("wl-gauge", box{6, 6, 2, 4}, onSensor(WidgetlabTemperatureKey),
				"Temperature", WidgetlabTemperatureKey, "C", WidgetlabSweepMin, WidgetlabSweepMax),
			buildAlarmCountWidget("wl-alarm-count", box{12, 6, 2, 4}, onZone, "Active alarms"),
			buildImageWidget("wl-image", box{18, 6, 2, 4},
				"Image", widgetlabImageDataURI, "A placeholder image", "contain"),

			buildTimeSeriesChartWidget("wl-chart", box{0, 14, 6, 8},
				onSensor(WidgetlabTemperatureKey, WidgetlabHumidityKey), "Temperature and humidity"),
			buildTableWidget("wl-table", box{14, 10, 6, 8}, onSensor(), "Latest values"),

			buildAlarmTableWidget("wl-alarm-table", box{0, 14, 14, 6}, onZone, "Alarms", widgetlabSlotSensor),
			buildCommandButtonWidget("wl-command", box{14, 10, 14, 6},
				widgetSubject{Slot: widgetlabSlotSensor}, "Setpoint"),

			buildEntitySelectorWidget("wl-zone-select", box{0, 12, 20, 2}, "Zone", widgetlabSlotZone),
			buildEntitySelectorWidget("wl-sensor-select", box{12, 12, 20, 2}, "Sensor", widgetlabSlotSensor),
		},
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal widgetlab gallery definition: %w", err)
	}
	return string(raw), nil
}

// ---- The stress board ---------------------------------------------------------

// buildWidgetlabStress renders the curated set bound to an edge sensor — the
// devices the pathological lane drives into the states a widget renders badly.
//
// It carries a label saying so. A maintainer-facing board that looks broken and
// does not say it is deliberately broken is a support ticket waiting to happen, and
// this one is reachable by anyone who bootstraps the scenario.
func buildWidgetlabStress(devices []DeviceInstance) (string, error) {
	edge, err := firstOfType(devices, WidgetlabEdgeDeviceTypeToken)
	if err != nil {
		return "", err
	}

	onEdge := func(measurements ...string) widgetSubject {
		return widgetSubject{Slot: widgetlabSlotEdge, Measurements: measurements}
	}

	def := dashboardDefinition{
		SchemaVersion: 1,
		Title:         "Widget Lab — Stress",
		Canvas:        widgetlabCanvas(),
		Slots: map[string]dashboardSlot{
			widgetlabSlotEdge: {
				Type:           "device",
				Label:          "Edge sensor",
				DefaultBinding: &dashboardSlotBinding{Kind: "device", DeviceToken: edge.Token},
			},
		},
		Widgets: []dashboardWidget{
			buildLabelWidget("wl-stress-title", box{0, 24, 0, 2},
				"Maintainer board: these widgets are bound to the edge sensors and are meant to be fed "+
					"deliberately pathological data. The generators that do that are not built yet, so "+
					"what you see below is still nominal.",
				"left", 16),

			// A chart of a device that spikes and goes silent: the axis blows out and
			// the line stops.
			buildTimeSeriesChartWidget("wl-stress-chart", box{0, 14, 2, 8},
				onEdge(WidgetlabTemperatureKey), "Spikes and silence"),
			// A card with no precision cap, so a raw float renders at full width.
			{
				Id: "wl-stress-card", Type: "latest-card", Layout: box{14, 10, 2, 4}.layout(),
				Datasource: onEdge(WidgetlabTemperatureKey).datasource(),
				Options: map[string]any{
					"title": "Unrounded", "measurement": WidgetlabTemperatureKey, "unit": "C",
				},
			},
			// A gauge whose scale is the nominal sweep while the device leaves it: the
			// needle pins at an end rather than tracking.
			buildGaugeWidget("wl-stress-gauge", box{14, 10, 6, 4}, onEdge(WidgetlabTemperatureKey),
				"Out of range", WidgetlabTemperatureKey, "C", WidgetlabSweepMin, WidgetlabSweepMax),
			// A table of a device that reports only some of its metrics.
			buildTableWidget("wl-stress-table", box{0, 24, 10, 6}, onEdge(), "Partial reports"),
		},
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal widgetlab stress definition: %w", err)
	}
	return string(raw), nil
}
