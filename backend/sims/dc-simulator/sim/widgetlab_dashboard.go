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
	// One slot per pathology, because each is a DIFFERENT device. A single "edge"
	// slot meant every widget on the stress board read the same sensor, so the board
	// showed one pathology under three titles.
	widgetlabSlotExtremes = "edgeExtremes"
	widgetlabSlotPartial  = "edgePartial"
	widgetlabSlotSilent   = "edgeSilent"
)

// widgetSubject is what a builder binds to: a slot name plus the measurements it
// selects. An empty Slot means the widget carries no datasource at all (label,
// image, entity-selector), which is a different thing from a slot selecting no
// measurements (the table's "every measurement" view).
type widgetSubject struct {
	Slot         string
	Measurements []string
	// Location names the location series this subject binds, when it binds one.
	// Empty means the selector names NO location series — which is the correct wire
	// form for every widget that is not a map, and is what every definition authored
	// before the location channel existed carries. The field is emitted only when
	// set, so a measurement widget serializes byte-identically to how it always did.
	Location string
}

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
	ds := &dashboardDatasource{Kind: "slot", Slot: s.Slot, Measurements: measurements}
	if s.Location != "" {
		ds.Location = &dashboardLocationSelection{Series: s.Location}
	}
	return ds
}

// box is a widget's grid placement, in cells: its start column, how many columns it covers,
// its start row and how many rows. Named so a board reads as a layout rather than as four
// positional integers per line.
type box struct{ col, cols, row, rows int }

func (b box) layout() map[string]dashboardPlacement {
	return baseLayout(b.col, b.cols, b.row, b.rows)
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
		Options: map[string]any{"title": title, "windowSeconds": widgetlabChartWindowSeconds},
	}
}

func buildLatestCardWidget(id string, b box, s widgetSubject, title, measurement, unit string, precision int, flash bool) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "latest-card", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit,
			"precision": precision, "flashOnChange": flash,
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
func buildAlarmCountWidget(id string, b box, s widgetSubject, title, severity string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "alarm-count", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "state": AlarmStateActiveWire, "severity": severity,
		},
	}
}

// commandName and parameterSchema are the SAME constants the profile's command
// definition is authored from. A button naming a command the published vocabulary
// does not contain renders a form, sends, and fails at the delivery boundary.
func buildCommandButtonWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: commandWidgetType, Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "commandName": WidgetlabCommandKey, "commandLabel": "Set Setpoint",
			"parameterSchema": widgetlabParameterSchema, "maxRows": 10,
		},
	}
}

// buildMapWidget renders the bound subject's LAST KNOWN POSITION.
//
// The subject names the location series explicitly: the hub resolves a location
// scope only for a selector that asks for one, so a map bound to a measurement-only
// subject renders its empty state forever. That is deliberate on the frontend side
// — it keeps the field observable — and it means this datasource is the thing being
// exercised, not decoration.
//
// No tileUrl is set, and that is the point rather than an omission. A board that
// names no tile source of its own inherits the tenant's basemap (ADR-079), which is
// what a real board should do and what the gallery should therefore demonstrate —
// so this widget draws on whatever the instance is actually configured with,
// including the provider that ships by default.
func buildMapWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "map", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{"title": title},
	}
}

func buildEntitySelectorWidget(id string, b box, title, target string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "entity-selector", Layout: b.layout(),
		Options: map[string]any{"title": title, "selectionTarget": target},
	}
}

// ---- Levels and indicators ----------------------------------------------------
//
// The five single-value DOM/SVG widgets. A threshold names a severity LEVEL, never a colour,
// so these carry none; `normal` keeps each widget's own neutral.

// widgetThreshold is one entry of the shared `thresholds` option: a band that starts at Value.
type widgetThreshold struct {
	Value float64 `json:"value"`
	Level string  `json:"level"`
	Label string  `json:"label,omitempty"`
}

func buildLinearGaugeWidget(id string, b box, s widgetSubject, title, measurement, unit, style string, min, max float64, thresholds []widgetThreshold) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "linear-gauge", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit, "style": style,
			"min": min, "max": max, "precision": 1, "thresholds": thresholds,
		},
	}
}

func buildLevelGaugeWidget(id string, b box, s widgetSubject, title, measurement, unit, readout string, min, max float64, thresholds []widgetThreshold) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "level-gauge", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit, "readout": readout,
			"min": min, "max": max, "precision": 0, "thresholds": thresholds,
		},
	}
}

// The battery names no min/max: its scale is the documented percent default, which is what the
// sim's battery metric is.
func buildBatteryWidget(id string, b box, s widgetSubject, title, measurement string, thresholds []widgetThreshold) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "battery", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": "%", "precision": 0, "thresholds": thresholds,
		},
	}
}

// onAt is omitted when nil, so the lamp is coloured by its bands alone.
func buildStatusLightWidget(id string, b box, s widgetSubject, title, measurement, unit string, onAt *float64, thresholds []widgetThreshold) dashboardWidget {
	options := map[string]any{"title": title, "measurement": measurement, "unit": unit, "precision": 1}
	if onAt != nil {
		options["onAt"] = *onAt
	}
	if len(thresholds) > 0 {
		options["thresholds"] = thresholds
	}
	return dashboardWidget{Id: id, Type: "status-light", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

// One bar per threshold, so the thresholds ARE the steps of the signal.
func buildSignalBarsWidget(id string, b box, s widgetSubject, title, measurement, unit string, thresholds []widgetThreshold) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "signal-bars", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit, "precision": 0, "thresholds": thresholds,
		},
	}
}

// ---- The radial family ----------------------------------------------------------

func buildCompassWidget(id string, b box, s widgetSubject, title, measurement string, thresholds []widgetThreshold) dashboardWidget {
	options := map[string]any{"title": title, "measurement": measurement, "precision": 0}
	if len(thresholds) > 0 {
		options["thresholds"] = thresholds
	}
	return dashboardWidget{Id: id, Type: "compass", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

func buildProgressRingWidget(id string, b box, s widgetSubject, title, measurement, unit, readout string, min, max float64, thresholds []widgetThreshold) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "progress-ring", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "measurement": measurement, "unit": unit, "readout": readout,
			"min": min, "max": max, "precision": 0, "thresholds": thresholds,
		},
	}
}

// ---- Shared board scaffolding -------------------------------------------------

// widgetlabChartWindowSeconds is the span the gallery's charts are PINNED to, so a viewer's
// time-range picker leaves them alone: a gallery chart exists to show the sweep turn around, and
// an hour (the default) or a week of it would flatten the curve into a blur.
//
// Fifteen minutes holds a full sweep (WidgetlabSweepTicks samples) at any emit interval up to ten
// seconds, which is enough to watch the curve rise and fall rather than a rising line whose shape
// a viewer has to take on trust.
const widgetlabChartWindowSeconds = 15 * 60

// A 1x1 transparent PNG. Inline for the reason buildImageWidget explains, and the
// smallest thing that proves the widget renders an image rather than its empty state.
const widgetlabImageDataURI = "data:image/png;base64," +
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func widgetlabCanvas() dashboardCanvas {
	return dashboardCanvas{
		Grid:        dashboardCanvasGrid(),
		Sizing:      "fill",
		Breakpoints: map[string]int{"base": 0},
	}
}

// deviceAreaToken returns the area a device is assigned to — the root context a board
// scoped to that area is built around. Shared by every scenario's board builder and by
// the sample gate; a device carries at most one area assignment (renderAssignments).
//
// Nothing enforces that a given device HAS one. Widgetlab's edge sensors were
// deliberately taken out of the zones so their pathological alarms stay off the
// zone-scoped catalog, and an earlier version of this comment claimed Validate
// enforced an area, which was true before that change and is not now. So this returns
// an error rather than assuming, and callers must pass a device that has one.
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
		SchemaVersion: dashboardSchemaVersion,
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
			buildLabelWidget("wl-title", box{0, 12, 0, 1},
				"Widget Lab — one of every dashboard widget", "left", 20),

			buildLatestCardWidget("wl-card", box{0, 3, 1, 3}, onSensor(WidgetlabTemperatureKey),
				"Temperature", WidgetlabTemperatureKey, "C", 1, true),
			buildGaugeWidget("wl-gauge", box{3, 3, 1, 3}, onSensor(WidgetlabTemperatureKey),
				"Temperature", WidgetlabTemperatureKey, "C", WidgetlabSweepMin, WidgetlabSweepMax),
			buildAlarmCountWidget("wl-alarm-count", box{6, 3, 1, 3}, onZone, "Active alarms", WidgetlabAlarmSeverityWire),
			buildImageWidget("wl-image", box{9, 3, 1, 3},
				"Image", widgetlabImageDataURI, "A placeholder image", "contain"),

			buildTimeSeriesChartWidget("wl-chart", box{0, 7, 4, 4},
				onSensor(WidgetlabTemperatureKey, WidgetlabHumidityKey), "Temperature and humidity"),
			buildTableWidget("wl-table", box{7, 5, 4, 4}, onSensor(), "Latest values"),

			buildAlarmTableWidget("wl-alarm-table", box{0, 7, 8, 3}, onZone, "Alarms", widgetlabSlotSensor),
			buildCommandButtonWidget("wl-command", box{7, 5, 8, 3},
				widgetSubject{Slot: widgetlabSlotSensor}, "Setpoint"),

			buildMapWidget("wl-map", box{0, 12, 11, 3},
				widgetSubject{Slot: widgetlabSlotSensor, Location: "latest"}, "Where the sensors are"),

			buildEntitySelectorWidget("wl-zone-select", box{0, 6, 14, 1}, "Zone", widgetlabSlotZone),
			buildEntitySelectorWidget("wl-sensor-select", box{6, 6, 14, 1}, "Sensor", widgetlabSlotSensor),
		},
	}
	// Each widget-family slice appends its own block below, starting at its own reserved row,
	// so two slices never place a widget on the same cells.
	def.Widgets = append(def.Widgets, widgetlabLevelsAndIndicators(onSensor)...)
	def.Widgets = append(def.Widgets, widgetlabRadialFamily(onSensor)...)
	def.Widgets = append(def.Widgets, widgetlabCommandControls(widgetSubject{Slot: widgetlabSlotSensor})...)
	def.Widgets = append(def.Widgets, widgetlabAggregates(onSensor)...)
	def.Widgets = append(def.Widgets, widgetlabFleet()...)
	def.Widgets = append(def.Widgets, widgetlabAlarmSeverity(onZone)...)
	def.Widgets = append(def.Widgets, widgetlabPresence(onSensor(), onZone)...)

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal widgetlab gallery definition: %w", err)
	}
	return string(raw), nil
}

// widgetlabLevelsRow is the first row of the levels-and-indicators block (rows 15-19), under
// the selectors that end the original gallery.
const widgetlabLevelsRow = 15

// widgetlabTemperatureBands are the severity bands the sweep crosses: normal from the bottom of
// the sweep, warning from 28, critical from 32. Shared by the gallery and the stress board so the
// out-of-range case reads against the same bands.
var widgetlabTemperatureBands = []widgetThreshold{
	{Value: WidgetlabSweepMin, Level: "normal"},
	{Value: 28, Level: "warning"},
	{Value: 32, Level: "critical"},
}

// widgetlabLevelsAndIndicators places the level and indicator widgets over the nominal sensor:
// the sweep drives the temperature ones through their bands, the battery metric counts down
// through the critical band, and humidity moves the tank and the signal steps.
func widgetlabLevelsAndIndicators(onSensor func(measurements ...string) widgetSubject) []dashboardWidget {
	const r = widgetlabLevelsRow
	temperatureBands := widgetlabTemperatureBands
	// A low battery is the abnormal end: critical from empty, then normal from 30.
	batteryBands := []widgetThreshold{
		{Value: 0, Level: "critical"},
		{Value: 15, Level: "warning"},
		{Value: 30, Level: "normal"},
	}
	halfOn := 50.0
	return []dashboardWidget{
		buildLinearGaugeWidget("wl-linear", box{0, 4, r, 3}, onSensor(WidgetlabTemperatureKey),
			"Temperature", WidgetlabTemperatureKey, "C", "bar", WidgetlabSweepMin, WidgetlabSweepMax, temperatureBands),
		buildLinearGaugeWidget("wl-thermometer", box{4, 3, r, 4}, onSensor(WidgetlabTemperatureKey),
			"Thermometer", WidgetlabTemperatureKey, "C", "thermometer", WidgetlabSweepMin, WidgetlabSweepMax, temperatureBands),
		buildLevelGaugeWidget("wl-level", box{7, 3, r, 4}, onSensor(WidgetlabHumidityKey),
			"Tank level", WidgetlabHumidityKey, "%", "percent", 0, 100, []widgetThreshold{
				{Value: 0, Level: "critical"},
				{Value: 30, Level: "warning"},
				{Value: 45, Level: "normal"},
			}),
		buildBatteryWidget("wl-battery", box{10, 2, r, 2}, onSensor(WidgetlabBatteryKey), "Battery", WidgetlabBatteryKey, batteryBands),
		buildStatusLightWidget("wl-status", box{10, 2, r + 2, 2}, onSensor(WidgetlabTemperatureKey),
			"Status", WidgetlabTemperatureKey, "C", nil, []widgetThreshold{
				{Value: WidgetlabSweepMin, Level: "normal", Label: "Normal"},
				{Value: 30, Level: "major", Label: "Hot"},
			}),
		buildStatusLightWidget("wl-status-onoff", box{2, 2, r + 3, 2}, onSensor(WidgetlabBatteryKey),
			"Powered", WidgetlabBatteryKey, "%", &halfOn, nil),
		buildSignalBarsWidget("wl-signal", box{0, 2, r + 3, 2}, onSensor(WidgetlabHumidityKey),
			"Signal", WidgetlabHumidityKey, "%", []widgetThreshold{
				{Value: 0, Level: "critical"},
				{Value: 42, Level: "warning"},
				{Value: 46, Level: "normal"},
				{Value: 52, Level: "normal"},
				{Value: 58, Level: "normal"},
			}),
	}
}

// widgetlabRadialRow is the first row of the radial-family block (rows 20-23), under the levels
// and indicators.
const widgetlabRadialRow = 20

// widgetlabRadialFamily places the compass and the progress rings. The sim reports no bearing,
// so the compass is bound to the battery countdown (100 down to 1) and titled as that stand-in,
// so no one reads it as a bearing; it sweeps the needle through a quarter of the dial; the stress board drives it past the ends of 0 to 360. The
// rings read humidity as a percent of 0..100 and the temperature sweep as a value on its own scale.
func widgetlabRadialFamily(onSensor func(measurements ...string) widgetSubject) []dashboardWidget {
	const r = widgetlabRadialRow
	return []dashboardWidget{
		buildCompassWidget("wl-compass", box{0, 4, r, 4}, onSensor(WidgetlabBatteryKey), "Battery as a heading", WidgetlabBatteryKey, nil),
		buildProgressRingWidget("wl-ring", box{4, 3, r, 3}, onSensor(WidgetlabHumidityKey),
			"Humidity", WidgetlabHumidityKey, "%", "percent", 0, 100, []widgetThreshold{
				{Value: 0, Level: "critical"},
				{Value: 30, Level: "warning"},
				{Value: 45, Level: "normal"},
			}),
		buildProgressRingWidget("wl-ring-value", box{7, 3, r, 3}, onSensor(WidgetlabTemperatureKey),
			"Temperature", WidgetlabTemperatureKey, "C", "value", WidgetlabSweepMin, WidgetlabSweepMax, widgetlabTemperatureBands),
	}
}

// ---- Command controls ---------------------------------------------------------

// widgetlabControlsRow is the first row of the command-controls block (rows 25-26). Rows 20-24
// belong to the radial family; keeping this block clear of them is what lets two slices append to
// the same gallery without ever placing a widget on the same cells.
const widgetlabControlsRow = 25

// buildCommandSliderWidget binds one numeric parameter of the published command. The range is NOT
// an option: the control reads it from the baked parameter schema, the same snapshot the command
// button renders its form from. Only the step is authored.
func buildCommandSliderWidget(id string, b box, s widgetSubject, title, commandName, commandLabel, schema, parameter string, step float64) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: commandSliderWidgetType, Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "commandName": commandName, "commandLabel": commandLabel,
			"parameterSchema": schema, "parameter": parameter, "step": step,
		},
	}
}

func buildCommandToggleWidget(id string, b box, s widgetSubject, title, commandName, commandLabel, schema, parameter string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: commandToggleWidgetType, Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "commandName": commandName, "commandLabel": commandLabel,
			"parameterSchema": schema, "parameter": parameter,
		},
	}
}

// widgetlabCommandControls places the slider (default span and the smallest it may be) on the
// setpoint command and the toggle (default span and smallest) on the power command, over the
// nominal sensor. The sim's devices answer every command they are sent, so each control's
// "last sent" caption walks Pending to Delivered when it is used.
func widgetlabCommandControls(subject widgetSubject) []dashboardWidget {
	const r = widgetlabControlsRow
	return []dashboardWidget{
		buildCommandSliderWidget("wl-cmd-slider", box{0, 4, r, 2}, subject, "Setpoint",
			WidgetlabCommandKey, "Set Setpoint", widgetlabParameterSchema, WidgetlabSetpointParameter, 0.5),
		buildCommandSliderWidget("wl-cmd-slider-min", box{4, 3, r, 2}, subject, "Setpoint (smallest)",
			WidgetlabCommandKey, "Set Setpoint", widgetlabParameterSchema, WidgetlabSetpointParameter, 0.5),
		buildCommandToggleWidget("wl-cmd-toggle", box{7, 3, r, 2}, subject, "Power",
			WidgetlabPowerCommandKey, "Set Power", widgetlabPowerParameterSchema, WidgetlabPowerParameter),
		buildCommandToggleWidget("wl-cmd-toggle-min", box{10, 2, r, 2}, subject, "Power (smallest)",
			WidgetlabPowerCommandKey, "Set Power", widgetlabPowerParameterSchema, WidgetlabPowerParameter),
	}
}

// ---- Aggregates and categorical charts ----------------------------------------

// widgetlabAggregatesRow is the first row of the aggregate block (rows 27-41), under the command
// controls that end at row 26.
const widgetlabAggregatesRow = 27

// buildStatCardWidget shows one statistic of one measurement over a span. windowSeconds 0 leaves
// the card on the board's time range; a positive value pins it to that span.
func buildStatCardWidget(id string, b box, s widgetSubject, title, measurement, stat, unit string, precision, windowSeconds int, thresholds []widgetThreshold) dashboardWidget {
	options := map[string]any{
		"title": title, "measurement": measurement, "stat": stat, "unit": unit, "precision": precision,
	}
	if windowSeconds > 0 {
		options["windowSeconds"] = windowSeconds
	}
	if len(thresholds) > 0 {
		options["thresholds"] = thresholds
	}
	return dashboardWidget{Id: id, Type: "stat-card", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

func buildBarChartWidget(id string, b box, s widgetSubject, title, unit, sort string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "bar-chart", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "unit": unit, "precision": 1, "sort": sort,
		},
	}
}

func buildPieChartWidget(id string, b box, s widgetSubject, title, unit, sort string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "pie-chart", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{"title": title, "unit": unit, "precision": 0, "sort": sort},
	}
}

// widgetlabAggregates places the stat cards (every statistic, the board-range card and a pinned
// one, a banded maximum, and the smallest size) over the nominal sensor, then a bar chart (wide, so
// columns) and a taller one (rows, sorted) and the pies over the sensor's three measurements. The pies read temperature,
// humidity and battery, all non-negative, so they draw; the bars sort so the order is visible.
func widgetlabAggregates(onSensor func(measurements ...string) widgetSubject) []dashboardWidget {
	const r = widgetlabAggregatesRow
	temperature := onSensor(WidgetlabTemperatureKey)
	all := onSensor(WidgetlabTemperatureKey, WidgetlabHumidityKey, WidgetlabBatteryKey)
	// The average card also draws its trend; every other card is the card without one.
	avg := buildStatCardWidget("wl-stat-avg", box{0, 3, r, 3}, temperature, "Average temperature",
		WidgetlabTemperatureKey, "avg", "C", 1, 0, nil)
	avg.Options["sparkline"] = true
	return []dashboardWidget{
		avg,
		buildStatCardWidget("wl-stat-max", box{3, 3, r, 3}, temperature, "Peak temperature",
			WidgetlabTemperatureKey, "max", "C", 1, 0, widgetlabTemperatureBands),
		buildStatCardWidget("wl-stat-min", box{6, 3, r, 3}, temperature, "Lowest temperature",
			WidgetlabTemperatureKey, "min", "C", 1, 0, nil),
		buildStatCardWidget("wl-stat-count", box{9, 3, r, 3}, temperature, "Readings",
			WidgetlabTemperatureKey, "count", "", 0, 0, nil),
		buildStatCardWidget("wl-stat-sum", box{0, 3, r + 3, 3}, onSensor(WidgetlabHumidityKey), "Humidity total",
			WidgetlabHumidityKey, "sum", "%", 0, 0, nil),
		buildStatCardWidget("wl-stat-last", box{3, 3, r + 3, 3}, temperature, "Latest temperature",
			WidgetlabTemperatureKey, "last", "C", 1, 0, widgetlabTemperatureBands),
		buildStatCardWidget("wl-stat-pinned", box{6, 3, r + 3, 3}, temperature, "Average, last 5 min",
			WidgetlabTemperatureKey, "avg", "C", 1, 300, nil),
		buildStatCardWidget("wl-stat-min-size", box{9, 3, r + 3, 2}, temperature, "Peak (smallest)",
			WidgetlabTemperatureKey, "max", "C", 0, 0, nil),

		buildBarChartWidget("wl-bar", box{0, 6, r + 6, 4}, all, "Latest readings", "", "none"),
		buildBarChartWidget("wl-bar-rows", box{6, 4, r + 6, 5}, all, "Largest first", "", "descending"),
		buildPieChartWidget("wl-pie", box{0, 4, r + 11, 4}, all, "Share of the total", "", "none"),
		buildPieChartWidget("wl-pie-min", box{4, 3, r + 11, 3}, all, "Share (smallest)", "", "descending"),
	}
}

// ---- The fleet: one row or bar per device of the zone ---------------------------

// widgetlabFleetRow is the first row of the fleet block (rows 42-53), under the categorical charts
// that end at row 41.
const widgetlabFleetRow = 42

// buildFleetTableWidget draws one row per device of the subject's anchor and one column per
// measurement it selects. Each column carries its own heading, unit, precision and severity bands;
// a reading older than staleAfterSeconds is marked stale; the device name drills to the sensor slot.
func buildFleetTableWidget(id string, b box, s widgetSubject, title string, columns []map[string]any, thresholds []map[string]any, staleAfterSeconds int, sortBy string) dashboardWidget {
	options := map[string]any{
		"title": title, "columns": columns, "selectionTarget": widgetlabSlotSensor,
	}
	if staleAfterSeconds > 0 {
		options["staleAfterSeconds"] = staleAfterSeconds
	}
	if len(thresholds) > 0 {
		options["thresholds"] = thresholds
	}
	if sortBy != "" {
		options["sortBy"] = sortBy
		options["sortDescending"] = true
	}
	return dashboardWidget{Id: id, Type: "fleet-table", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

// buildFleetCountWidget counts the devices of the subject's anchor: those whose latest reading of
// the measurement is in an abnormal band of the thresholds ("attention"), or the devices themselves
// ("devices", which needs neither a measurement nor bands).
func buildFleetCountWidget(id string, b box, s widgetSubject, title, show, measurement string, thresholds []widgetThreshold) dashboardWidget {
	options := map[string]any{"title": title, "show": show}
	if measurement != "" {
		options["measurement"] = measurement
	}
	if len(thresholds) > 0 {
		options["thresholds"] = thresholds
	}
	return dashboardWidget{Id: id, Type: "fleet-count", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

// buildDeviceBarChartWidget is a bar chart with one bar per device of the subject's anchor, for
// one measurement.
func buildDeviceBarChartWidget(id string, b box, s widgetSubject, title, measurement, unit, sort string) dashboardWidget {
	return dashboardWidget{
		Id: id, Type: "bar-chart", Layout: b.layout(), Datasource: s.datasource(),
		Options: map[string]any{
			"title": title, "groupBy": "device", "measurement": measurement,
			"unit": unit, "precision": 1, "sort": sort,
		},
	}
}

// widgetlabFleet binds the fleet widgets to the zone, so each shows every sensor in it at once: a
// table with all three measurements, banded temperatures and a stale threshold; a smaller table
// at the size minimum; and a bar per sensor sorted by temperature.
func widgetlabFleet() []dashboardWidget {
	const r = widgetlabFleetRow
	zone := widgetSubject{
		Slot:         widgetlabSlotZone,
		Measurements: []string{WidgetlabTemperatureKey, WidgetlabHumidityKey, WidgetlabBatteryKey},
	}
	bands := make([]map[string]any, 0, len(widgetlabTemperatureBands))
	for _, t := range widgetlabTemperatureBands {
		bands = append(bands, map[string]any{"measurement": WidgetlabTemperatureKey, "value": t.Value, "level": t.Level})
	}
	columns := []map[string]any{
		{"measurement": WidgetlabTemperatureKey, "label": "Temperature", "unit": "C", "precision": 1},
		{"measurement": WidgetlabHumidityKey, "label": "Humidity", "unit": "%", "precision": 0},
		{"measurement": WidgetlabBatteryKey, "label": "Battery", "unit": "%", "precision": 0},
	}
	return []dashboardWidget{
		buildFleetTableWidget("wl-fleet-table", box{0, 12, r, 6}, zone, "Sensors in the zone", columns, bands, 300, WidgetlabTemperatureKey),
		buildFleetTableWidget("wl-fleet-table-min", box{0, 6, r + 6, 3},
			widgetSubject{Slot: widgetlabSlotZone, Measurements: []string{WidgetlabTemperatureKey}},
			"Fleet (smallest)", columns[:1], bands, 0, ""),
		buildDeviceBarChartWidget("wl-fleet-bar", box{6, 6, r + 6, 5}, zone, "Temperature by sensor",
			WidgetlabTemperatureKey, "C", "descending"),
		buildFleetCountWidget("wl-fleet-count", box{0, 3, r + 9, 3}, zone, "Sensors needing attention", "attention",
			WidgetlabTemperatureKey, widgetlabTemperatureBands),
		buildFleetCountWidget("wl-fleet-devices", box{3, 3, r + 9, 3}, zone, "Sensors in the zone", "devices", "", nil),
	}
}

// widgetlabAlarmSeverityRow is the first row of the alarm-severity block (rows 54-56), under the
// fleet block that ends at row 53.
const widgetlabAlarmSeverityRow = 54

// buildAlarmSeverityWidget breaks the subject's active alarms down by severity. A non-empty
// deviceType narrows it to the alarms on devices of that type.
func buildAlarmSeverityWidget(id string, b box, s widgetSubject, title, deviceType string) dashboardWidget {
	options := map[string]any{"title": title, "state": AlarmStateActiveWire}
	if deviceType != "" {
		options["deviceType"] = deviceType
	}
	return dashboardWidget{Id: id, Type: "alarm-severity", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

// widgetlabAlarmSeverity places the severity breakdown over the zone twice: every active alarm in
// it, and only those on devices of the gallery's sensor type. The sweep raises one severity, so
// the other four rows show the dimmed zero.
func widgetlabAlarmSeverity(onZone widgetSubject) []dashboardWidget {
	const r = widgetlabAlarmSeverityRow
	return []dashboardWidget{
		buildAlarmSeverityWidget("wl-alarm-severity", box{0, 6, r, 3}, onZone, "Active alarms by severity", ""),
		buildAlarmSeverityWidget("wl-alarm-severity-type", box{6, 6, r, 3}, onZone, "Sensor alarms by severity", WidgetlabDeviceTypeToken),
	}
}

// widgetlabPresenceRow is the first row of the presence block (rows 57-60), under the alarm-severity
// block that ends at row 56.
const widgetlabPresenceRow = 57

// buildDeviceStatusWidget shows one device's connectivity: Online or Offline, and when it was last
// seen. A staleAfterSeconds above zero marks the card stale once the device has been silent that long.
func buildDeviceStatusWidget(id string, b box, s widgetSubject, title string, staleAfterSeconds int) dashboardWidget {
	options := map[string]any{"title": title}
	if staleAfterSeconds > 0 {
		options["staleAfterSeconds"] = staleAfterSeconds
	}
	return dashboardWidget{Id: id, Type: "device-status", Layout: b.layout(), Datasource: s.datasource(), Options: options}
}

// buildOnlineDoughnutWidget rings the subject's anchor by connectivity: online, offline and no
// status yet.
func buildOnlineDoughnutWidget(id string, b box, s widgetSubject, title string) dashboardWidget {
	return dashboardWidget{Id: id, Type: "online-doughnut", Layout: b.layout(), Datasource: s.datasource(), Options: map[string]any{"title": title}}
}

// widgetlabPresence places the connectivity widgets: the status card on the sensor and the doughnut
// over the zone. Neither selects a measurement: both read device-state.
func widgetlabPresence(onSensor, onZone widgetSubject) []dashboardWidget {
	const r = widgetlabPresenceRow
	return []dashboardWidget{
		buildDeviceStatusWidget("wl-device-status", box{0, 3, r, 3}, onSensor, "Sensor status", 600),
		buildOnlineDoughnutWidget("wl-online-doughnut", box{3, 4, r, 4}, onZone, "Sensors online"),
	}
}

// ---- The stress board ---------------------------------------------------------

// buildWidgetlabStress renders the curated set bound to an edge sensor — the
// devices the pathological lane drives into the states a widget renders badly.
//
// It carries a label saying so. A maintainer-facing board that looks broken and
// does not say it is deliberately broken is a support ticket waiting to happen, and
// this one is reachable by anyone who bootstraps the scenario.
func buildWidgetlabStress(devices []DeviceInstance) (string, error) {
	// Bound by BEHAVIOUR, not by position: each widget below is titled for a specific
	// pathology, so the device it reads has to be the one that produces it. Binding
	// the first edge device to everything showed spikes under a title promising
	// silence and a table promising dropped rows that never dropped one.
	extremes, err := widgetlabEdgeDevice(devices, edgeExtremes)
	if err != nil {
		return "", err
	}
	partial, err := widgetlabEdgeDevice(devices, edgePartial)
	if err != nil {
		return "", err
	}
	silent, err := widgetlabEdgeDevice(devices, edgeSilent)
	if err != nil {
		return "", err
	}

	on := func(slot string, measurements ...string) widgetSubject {
		return widgetSubject{Slot: slot, Measurements: measurements}
	}
	deviceSlot := func(label string, d DeviceInstance) dashboardSlot {
		return dashboardSlot{
			Type:           "device",
			Label:          label,
			DefaultBinding: &dashboardSlotBinding{Kind: "device", DeviceToken: d.Token},
		}
	}

	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         "Widget Lab — Stress",
		Canvas:        widgetlabCanvas(),
		Slots: map[string]dashboardSlot{
			widgetlabSlotExtremes: deviceSlot("Extremes sensor", extremes),
			widgetlabSlotPartial:  deviceSlot("Partial reporter", partial),
			widgetlabSlotSilent:   deviceSlot("Silent sensor", silent),
		},
		Widgets: []dashboardWidget{
			// One line, because the label is one row high and a phone-width board (390 px, six
			// cells) has room for about 40 characters of it. Each widget below is bound to the
			// edge sensor that produces its own case: spikes and out-of-range values, dropped
			// metrics, and silence.
			buildLabelWidget("wl-stress-title", box{0, 12, 0, 1},
				"Pathological data, meant to look wrong",
				"left", 16),

			// The extremes device: a chart whose axis is blown out by a spike an order
			// of magnitude past the sweep, and a gauge scaled for the sweep that the
			// device leaves entirely, so the needle pins.
			buildTimeSeriesChartWidget("wl-stress-chart", box{0, 7, 1, 5},
				on(widgetlabSlotExtremes, WidgetlabTemperatureKey), "Spikes and out-of-range"),
			buildGaugeWidget("wl-stress-gauge", box{7, 5, 1, 3},
				on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Off the scale", WidgetlabTemperatureKey, "C", WidgetlabSweepMin, WidgetlabSweepMax),
			// No precision cap, so a raw float renders at its full width.
			{
				Id: "wl-stress-card", Type: "latest-card", Layout: box{7, 5, 4, 2}.layout(),
				Datasource: on(widgetlabSlotExtremes, WidgetlabTemperatureKey).datasource(),
				Options: map[string]any{
					"title": "Unrounded", "measurement": WidgetlabTemperatureKey, "unit": "C",
				},
			},

			// The partial reporter: rows appear and disappear underneath the table.
			buildTableWidget("wl-stress-table", box{0, 6, 6, 3}, on(widgetlabSlotPartial), "Partial reports"),
			// The silent device: the chart stops extending and the card's timestamp
			// goes stale while the device is simply gone.
			buildTimeSeriesChartWidget("wl-stress-silence", box{6, 6, 6, 3},
				on(widgetlabSlotSilent, WidgetlabTemperatureKey), "Goes silent"),

			// label and image carry no datasource, so a PLACEMENT is the only
			// pathological axis they have — there is no device behaviour to drive them
			// with. Text far longer than its box (which the frame CLIPS rather than
			// overflowing), and an image that fails to decode.
			buildLabelWidget("wl-stress-label", box{0, 6, 9, 2},
				"Pneumonoultramicroscopicsilicovolcanoconiosisantidisestablishmentarianismfloccinaucinihilipilification",
				"left", 18),
			// A data: URI whose payload is not valid base64, so the decode fails
			// locally. A broken http(s) URL would ALSO fail behind an air-gapped
			// deploy's egress policy, making a network block indistinguishable from
			// the render failure this is here to show.
			buildImageWidget("wl-stress-image", box{6, 6, 9, 2},
				"Broken image", "data:image/png;base64,this-is-not-base64", "An image that cannot decode", "cover"),

			// The extremes device again, on scale widgets whose range is the sweep's: a spike
			// ten times past the top, zero and a negative below the bottom. The fill pins to the
			// end and the readout still prints the true value with an off-scale marker.
			buildLinearGaugeWidget("wl-stress-linear", box{0, 6, 11, 3}, on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Off the scale (bar)", WidgetlabTemperatureKey, "C", "bar", WidgetlabSweepMin, WidgetlabSweepMax, widgetlabTemperatureBands),
			buildLevelGaugeWidget("wl-stress-level", box{6, 3, 11, 4}, on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Off the scale (tank)", WidgetlabTemperatureKey, "C", "value", WidgetlabSweepMin, WidgetlabSweepMax, widgetlabTemperatureBands),

			// The radial family on the same extremes device (rows 15 and down). The compass reads
			// the cycle as headings: 350 stays 350, and the negative step folds onto the dial
			// (-35 is 325) instead of sweeping the needle backwards. The ring is scaled for the
			// sweep, so it pins at both ends and marks the true reading.
			buildCompassWidget("wl-stress-compass", box{0, 4, 15, 4}, on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Headings that fold", WidgetlabTemperatureKey, nil),
			buildProgressRingWidget("wl-stress-ring", box{4, 3, 15, 3}, on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Off the scale (ring)", WidgetlabTemperatureKey, "C", "value", WidgetlabSweepMin, WidgetlabSweepMax, widgetlabTemperatureBands),

			// The same spike, read as a maximum: the figure is the spike itself, which the average
			// over the same span would hide.
			buildStatCardWidget("wl-stress-stat", box{0, 3, 20, 3}, on(widgetlabSlotExtremes, WidgetlabTemperatureKey),
				"Peak of the spikes", WidgetlabTemperatureKey, "max", "C", 1, 0, widgetlabTemperatureBands),
		},
	}

	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal widgetlab stress definition: %w", err)
	}
	return string(raw), nil
}
