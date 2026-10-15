// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// The sitepulse board, `sp-dashboard`: fleet fuel, haul-truck payload, the site's active
// alarms, machine positions and the crusher plant, laid out to sit beside the 3D window the
// Unity player renders.
//
// 🔴 THE BOARD ASSUMES ~960 px OF WIDTH (half a 1920 screen, the other half being the 3D
// window) AND 1080 px OF HEIGHT. It is not capped to one screen: the ORDERING RULE is that
// the alarms, the KPI row and the map end above the fold (sitepulseFoldRows), and the cards
// scroll below it, as many rows of them as the fleet needs. It is laid out in SQUARE cells on
// the 12-column grid: at 960 px a cell is ~73 px, comfortably above the 56 px minimum, so the
// board renders as authored there and every widget is inside the size its descriptor declares,
// which the frontend layout lint enforces over the committed fixture. On a full 1920 px screen
// the cells double and every widget only grows.
//
// 🔴 THE WIDGET LIBRARY HAS NO VIEW THAT DRAWS ONE MEASUREMENT ACROSS MANY DEVICES.
// useMeasurementStream keys its latest values by measurement NAME only, so a table or
// chart bound to the whole fleet would collapse eighteen trucks into one row, or draw
// them as one interleaved sawtooth. "Every machine's fuel" is therefore one tank (a
// level-gauge) per machine, and each hauler's payload one latest-card, each bound through a
// slot named for the device it shows. A per-device table mode would be the real answer; it
// is a widget-library change, not this board's, and it replaces these per-machine cards.
//
// 🔴 EVERY NEW WIDGET HERE IS BOUND TO A MEASUREMENT THE SIM REALLY REPORTS, and the ones
// that would need an invented number are absent on purpose. The sim reports fuel, engine
// temperature, engine hours, payload, tyre pressure and position per machine, and throughput
// and a running flag for the plant: no radio signal and no battery, so no signal-bars or
// battery widget; no heading, so no compass; and no shift target or plant capacity, so no
// progress-ring (the throughput stat pair stands in for it). Every band edge is a rule
// threshold, and there is deliberately no "warning" band below the overheat edge: no rule
// warns before it alarms.
//
// Engine temperature and tyre pressure are served the same honest way: the alarm table
// carries each machine's overheat and tyre alarms with the triggering value, the counts
// total them, and two gauges follow the SELECTED machine, which an alarm row's
// originator drill re-points.
//
// 🔴 THE SITE-WIDE ALARM SURFACES ARE BOUNDED, AND SAY SO HERE. A scoped alarm widget
// runs one alarms query PER member device (dashboards hub.ts queryAlarms), and an anchor
// resolves at most one page of members (dashboards resolver.ts ANCHOR_PAGE_SIZE, 500).
// At the S1 size that is 19 devices and 3 widgets, which is nothing. Under a large
// `--devices` run the board is both expensive and partial past 500 devices: it is the
// 18-machine demo's board, and the card bands below cap at one screen of rows per kind
// for the same reason (the default fleet of six per kind is well inside it). A tenant-wide
// alarm widget (no datasource) would avoid the fan-out but the widget gate requires every
// alarm widget to bind one.
//
// The Machine selector lists the anchor's members, which include the crusher: picking it
// points the two gauges at a device that reports neither metric, so they render empty.
// No anchor covers the machines only, and adding one is out of scope.
//
// 🔴 NO COMMAND WIDGET YET, which is S2's work (console dispatch of goto-area, bound to
// selectedMachine). Note that nothing but TestSitepulseBoardCarriesNoCommandWidgetYet
// keeps one off: Validate's control-widget gate refuses a command-button only under
// FarEndNone, and sitepulse is FarEndExternal, where a board offering control is exactly
// right because the Unity player is the device (TestValidateAllowsAControlBoardForAnExternalFarEnd).

const (
	SitepulseDashboardToken = "sp-dashboard"
	sitepulseDashboardName  = "Site Pulse"

	// `site` anchors the board to the contractor customer, which every device is assigned
	// to (renderAssignments), so it is the whole site, the plant included. `plant` is the
	// crusher. `selectedMachine` is the gauges' subject, scoped to the site.
	sitepulseSlotSite    = "site"
	sitepulseSlotPlant   = "plant"
	sitepulseSlotMachine = "selectedMachine"

	// The window the board is laid out for.
	sitepulseViewportWidth  = 960
	sitepulseViewportHeight = 1080

	// The fold: the whole rows a 1080 px-tall window shows. A cell is the width left after the
	// padding and the gutters, shared by the columns, and a row is as tall as a cell is wide, so
	// a row PITCH (cell + gutter) is (width - padding + gutter) / columns: 968 / 12 = ~80.7 px
	// at 960, and 1080 px holds 13 of them. The alarm surfaces, the KPI row and the map must END
	// at or above it; the rest may scroll. Integer arithmetic, rounded down: a row the window
	// shows only part of is below the fold.
	sitepulseFoldRows = (sitepulseViewportHeight - 2*dashboardGridPadding + dashboardGridGutter) * dashboardGridColumns /
		(sitepulseViewportWidth - 2*dashboardGridPadding + dashboardGridGutter)

	// Cards. A payload card is 4x2 (the latest-card minimum is 3x2), so three fill the 12 columns
	// and the default fleet's six haulers make two FULL rows. A fuel tank is 2x3 (the level-gauge
	// minimum), so six fill the 12 columns and the default fleet's six machines per kind make ONE
	// full row of tanks. Every machine gets its card: the Unity demo's story runs through all of
	// them (SP-HL-0006 is the low-fuel truck). A kind wraps onto as many rows as it needs, up to
	// one screen of rows of the PAYLOAD card (the taller-per-machine band, so the two bands show
	// the same number of machines); the cap is DERIVED from the grid, not chosen, and only a large
	// `--devices` run reaches it, when the label says the rows are partial.
	sitepulseCardCols        = 4
	sitepulseCardRows        = 2
	sitepulseCardsPerRow     = dashboardGridColumns / sitepulseCardCols
	sitepulseCardRowsPerKind = sitepulseFoldRows / sitepulseCardRows
	sitepulseCardsPerKind    = sitepulseCardsPerRow * sitepulseCardRowsPerKind

	sitepulseTankCols    = 2
	sitepulseTankRows    = 3
	sitepulseTanksPerRow = dashboardGridColumns / sitepulseTankCols

	// The board's time range, the span its aggregate cards and charts cover until a viewer picks
	// another: an hour is long enough for a plant average to mean something and short enough to
	// follow a shift's trouble. Stated explicitly (it is also the unauthored default) so the
	// header's range control is part of what this board is declared to carry.
	sitepulseTimeRangeSeconds = 3600

	// Rows. The KPIs (3 rows: the crusher lamp needs three to read at the smallest cell) lead, then the alarm table and the map stack on the left 8
	// columns beside the machine selector and its two gauges on the right 4, in a band that
	// ends at row 10; the fuel label (1 row: it is just "Fuel level" now that the low-fuel
	// threshold lives on the tanks) and the tank and card bands follow.
	sitepulseKPIRows      = 3
	sitepulseFuelLabelRow = sitepulseKPIRows + 7
	sitepulseFuelFirstRow = sitepulseFuelLabelRow + 1

	// Display scales for the engine gauge, the tyre gauge and the fuel tanks. Not contract
	// values: the only fixed points are the rule thresholds, and each scale keeps its threshold
	// well inside the arc.
	sitepulseEngineTempGaugeMax = 130.0
	sitepulseTyreGaugeMax       = 1000.0
	sitepulseFuelTankMax        = 100.0
)

// sitepulseMachineKinds is the machine device types in the order their card rows stack.
var sitepulseMachineKinds = []string{SitepulseDozerTypeToken, SitepulseLoaderTypeToken, SitepulseHaulerTypeToken}

// sitepulseMachinesOfType filters devices to one type, preserving order.
func sitepulseMachinesOfType(devices []DeviceInstance, typ string) []DeviceInstance {
	var out []DeviceInstance
	for _, d := range devices {
		if d.DeviceTypeToken == typ {
			out = append(out, d)
		}
	}
	return out
}

// sitepulseFuelBandText is the label over the fuel rows. The low-fuel threshold is no longer
// prose here: it is a `thresholds` entry on each card, built from the constant the rule is
// built from (sitepulseApplyThresholds), so the board cannot state a number the rule does not
// use. perKind is the largest machine population; when it exceeds the cards shown the
// label says the rows are partial rather than letting them look complete.
func sitepulseFuelBandText(shown, perKind int) string {
	text := "Fuel level"
	if perKind > shown {
		text += fmt.Sprintf(" · showing the first %d of %d per machine kind", shown, perKind)
	}
	return text
}

// sitepulseCardBandRows is the grid rows a band of n cards takes once it wraps.
func sitepulseCardBandRows(n int) int {
	return (n + sitepulseCardsPerRow - 1) / sitepulseCardsPerRow * sitepulseCardRows
}

// sitepulseTankBandRows is the grid rows a band of n fuel tanks takes once it wraps.
func sitepulseTankBandRows(n int) int {
	return (n + sitepulseTanksPerRow - 1) / sitepulseTanksPerRow * sitepulseTankRows
}

// sitepulseTankBox places the i-th tank of a band that starts at row.
func sitepulseTankBox(row, i int) box {
	return box{sitepulseTankCols * (i % sitepulseTanksPerRow), sitepulseTankCols, row + sitepulseTankRows*(i/sitepulseTanksPerRow), sitepulseTankRows}
}

// sitepulseCardBox places the i-th card of a band that starts at row: left to right, then
// onto the next row.
func sitepulseCardBox(row, i int) box {
	return box{sitepulseCardCols * (i % sitepulseCardsPerRow), sitepulseCardCols, row + sitepulseCardRows*(i/sitepulseCardsPerRow), sitepulseCardRows}
}

func sitepulseDeviceSlot(label string, d DeviceInstance, scope *dashboardScope) dashboardSlot {
	return dashboardSlot{
		Type:           "device",
		Label:          label,
		DefaultBinding: &dashboardSlotBinding{Kind: "device", DeviceToken: d.Token},
		Scope:          scope,
	}
}

// buildSitepulseDashboard renders the board from a (resized) device population, so it
// never binds a machine the run does not provision.
func buildSitepulseDashboard(devices []DeviceInstance) (string, error) {
	plant, err := firstOfType(devices, SitepulsePlantTypeToken)
	if err != nil {
		return "", err
	}
	firstDozer, err := firstOfType(devices, SitepulseDozerTypeToken)
	if err != nil {
		return "", err
	}

	slots := map[string]dashboardSlot{
		sitepulseSlotSite: {
			Type:  "anchor",
			Label: "Site",
			DefaultBinding: &dashboardSlotBinding{
				Kind: "anchor",
				Anchor: &dashboardAnchorTarget{
					Relationship: assignmentRelationshipType,
					TargetType:   "customer",
					TargetToken:  SitepulseCustomerToken,
				},
			},
		},
		sitepulseSlotPlant:   sitepulseDeviceSlot("Crusher plant", plant, nil),
		sitepulseSlotMachine: sitepulseDeviceSlot("Machine", firstDozer, &dashboardScope{Parent: sitepulseSlotSite, Strategy: "manual"}),
	}

	onSite := widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{}}
	onPlant := func(metric string) widgetSubject {
		return widgetSubject{Slot: sitepulseSlotPlant, Measurements: []string{metric}}
	}
	onMachine := func(metric string) widgetSubject {
		return widgetSubject{Slot: sitepulseSlotMachine, Measurements: []string{metric}}
	}

	// The crusher lamp: lit while plant_running reports 1 (onAt), dark at 0. No threshold bands:
	// no rule fires on the plant, so no band would have a source. No unit either (it is a flag).
	onAt := 1.0
	running := buildStatusLightWidget("sp-plant-running", box{4, 2, 0, sitepulseKPIRows}, onPlant(SitepulsePlantRunningKey),
		"Crusher running", SitepulsePlantRunningKey, "", &onAt, nil)
	running.Options["precision"] = 0
	// An empty unit is no unit: omit the key rather than author a blank one.
	delete(running.Options, "unit")

	alarms := buildAlarmTableWidget("sp-alarms", box{0, 8, sitepulseKPIRows, 3}, onSite, "Active alarms", sitepulseSlotMachine)
	// ACTIVE only: this is an operator board, and a cleared row must not push a live
	// critical off a 25-row page. The raise-then-clear story shows on the counts and cards.
	alarms.Options["state"] = AlarmStateActiveWire

	// The throughput pair covers the board's time range (windowSeconds 0): the card captions
	// itself "Average · 1h" from the span in force, so the title stays the measurement's name.
	// No thresholds: no rule fires on throughput, and the sim defines no target or capacity, so
	// there is no honest progress-ring either.
	widgets := []dashboardWidget{
		buildAlarmCountWidget("sp-alarms-critical", box{0, 2, 0, sitepulseKPIRows}, onSite, "Critical alarms", AlarmSeverityCriticalWire),
		buildAlarmCountWidget("sp-alarms-major", box{2, 2, 0, sitepulseKPIRows}, onSite, "Major alarms", AlarmSeverityMajorWire),
		running,
		buildStatCardWidget("sp-plant-throughput-avg", box{6, 3, 0, sitepulseKPIRows}, onPlant(SitepulseThroughputKey),
			"Avg throughput", SitepulseThroughputKey, "avg", "t/h", 0, 0, nil),
		buildStatCardWidget("sp-plant-throughput-max", box{9, 3, 0, sitepulseKPIRows}, onPlant(SitepulseThroughputKey),
			"Peak throughput", SitepulseThroughputKey, "max", "t/h", 0, 0, nil),
	}

	// Card bands, one per kind, each wrapping onto the rows it needs; the label says when
	// the per-kind cap leaves a band partial.
	shown, perKind := 0, 0
	type band struct {
		devices []DeviceInstance
		row     int
	}
	var fuelBands []band
	row := sitepulseFuelFirstRow
	for _, kind := range sitepulseMachineKinds {
		all := sitepulseMachinesOfType(devices, kind)
		if len(all) > perKind {
			perKind = len(all)
		}
		n := len(all)
		if n > sitepulseCardsPerKind {
			n = sitepulseCardsPerKind
		}
		if n > shown {
			shown = n
		}
		fuelBands = append(fuelBands, band{devices: all[:n], row: row})
		row += sitepulseTankBandRows(n)
	}

	widgets = append(widgets, buildLabelWidget("sp-fuel-label", box{0, dashboardGridColumns, sitepulseFuelLabelRow, sitepulseFuelFirstRow - sitepulseFuelLabelRow}, sitepulseFuelBandText(shown, perKind), "left", 14))

	// The low-fuel bands are the rule's own: the rule's severity from empty, normal from the
	// threshold it fires below (a "below X" rule is two entries, because a band runs to the next).
	lowFuel := []widgetThreshold{
		{Value: 0, Level: SitepulseSeverity},
		{Value: SitepulseLowFuelThreshold, Level: "normal"},
	}
	for _, b := range fuelBands {
		for i, d := range b.devices {
			slots[d.Token] = sitepulseDeviceSlot(d.ExternalId, d, nil)
			widgets = append(widgets, buildLevelGaugeWidget("sp-fuel-"+d.Token, sitepulseTankBox(b.row, i),
				widgetSubject{Slot: d.Token, Measurements: []string{SitepulseFuelKey}},
				"Fuel %", SitepulseFuelKey, "%", "value", 0, sitepulseFuelTankMax, lowFuel))
		}
	}

	haulers := sitepulseMachinesOfType(devices, SitepulseHaulerTypeToken)
	if len(haulers) > sitepulseCardsPerKind {
		haulers = haulers[:sitepulseCardsPerKind]
	}
	for i, d := range haulers {
		slots[d.Token] = sitepulseDeviceSlot(d.ExternalId, d, nil)
		widgets = append(widgets, buildLatestCardWidget("sp-payload-"+d.Token, sitepulseCardBox(row, i),
			widgetSubject{Slot: d.Token, Measurements: []string{SitepulsePayloadKey}},
			"Payload", SitepulsePayloadKey, "t", 1, false))
	}

	widgets = append(widgets,
		alarms,
		buildMapWidget("sp-map", box{0, 8, sitepulseKPIRows + 3, 4},
			widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{}, Location: "latest"}, "Machine positions"),
		buildEntitySelectorWidget("sp-machine-select", box{8, 4, sitepulseKPIRows, 1}, "Machine", sitepulseSlotMachine),
		// The overheat rule fires at one number, so the gauge has one band edge there. No warning
		// band: nothing in the sim warns before the alarm, and an invented one would colour a
		// reading the platform does not call a problem.
		buildLinearGaugeWidget("sp-engine-temp", box{8, 4, sitepulseKPIRows + 1, 3}, onMachine(SitepulseEngineTempKey),
			"Engine temp", SitepulseEngineTempKey, "°C", "bar", 0, sitepulseEngineTempGaugeMax, []widgetThreshold{
				{Value: 0, Level: "normal"},
				{Value: SitepulseOverheatThreshold, Level: SitepulseOverheatSeverity},
			}),
		withThresholds(buildGaugeWidget("sp-tyre", box{8, 4, sitepulseKPIRows + 4, 3}, onMachine(SitepulseTyreKey),
			"Tyre pressure",
			SitepulseTyreKey, "kPa", 0, sitepulseTyreGaugeMax),
			dashboardThreshold{Value: 0, Level: SitepulseTyreSeverity},
			dashboardThreshold{Value: SitepulseTyrePressureThreshold, Level: "normal"}),
	)

	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         sitepulseDashboardName,
		Canvas:        widgetlabCanvas(),
		Slots:         slots,
		Widgets:       widgets,
		TimeRange:     &dashboardTimeRange{DefaultSeconds: sitepulseTimeRangeSeconds},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal sitepulse dashboard definition: %w", err)
	}
	return string(raw), nil
}
