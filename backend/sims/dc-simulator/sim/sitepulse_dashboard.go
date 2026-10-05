// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// The sitepulse board, `sp-dashboard`: fleet fuel, haul-truck payload, the site's active
// alarms, machine positions and the crusher plant, sized to sit on one screen beside the
// 3D window the Unity player renders.
//
// 🔴 THE WIDGET LIBRARY HAS NO VIEW THAT DRAWS ONE MEASUREMENT ACROSS MANY DEVICES.
// useMeasurementStream keys its latest values by measurement NAME only, so a table or
// chart bound to the whole fleet would collapse eighteen trucks into one row, or draw
// them as one interleaved sawtooth. "Every machine's fuel" is therefore one latest-card
// per machine, each bound through a slot named for the device it shows. A per-device
// table mode would be the real answer; it is a widget-library change, not this board's.
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
// 18-machine demo's board, and the card bands below cap at one row per kind for the
// same reason. A tenant-wide alarm widget (no datasource) would avoid the fan-out but
// the widget gate requires every alarm widget to bind one.
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

	// One row of cards per machine kind, so the per-kind cap is DERIVED from the grid
	// rather than chosen: six four-column cards fill the 24 columns.
	sitepulseCardColSpan  = 4
	sitepulseCardsPerKind = dashboardGridColumns / sitepulseCardColSpan

	// Display scales for the two gauges. Not contract values: the only fixed points are
	// the rule thresholds, and each scale keeps its threshold well inside the arc.
	sitepulseEngineTempGaugeMax = 130.0
	sitepulseTyreGaugeMax       = 1000.0
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

// sitepulseFuelBandText is the label over the fuel rows. The threshold is read from the
// constant the rule is built from, so the board cannot state a number the rule does not
// use. perKind is the largest machine population; when it exceeds the cards shown the
// label says the rows are partial rather than letting them look complete.
func sitepulseFuelBandText(shown, perKind int) string {
	text := fmt.Sprintf("Fuel level — the low-fuel alarm and refuel command fire below %g %%", SitepulseLowFuelThreshold)
	if perKind > shown {
		text += fmt.Sprintf(" · showing the first %d of %d per machine kind", shown, perKind)
	}
	return text
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

	running := buildLatestCardWidget("sp-plant-running", box{18, 6, 0, 3}, onPlant(SitepulsePlantRunningKey),
		"Crusher running (1 = yes, 0 = no)", SitepulsePlantRunningKey, "", 0, false)
	// An empty unit is no unit: omit the key rather than author a blank one.
	delete(running.Options, "unit")

	alarms := buildAlarmTableWidget("sp-alarms", box{0, 12, 12, 8}, onSite, "Active alarms", sitepulseSlotMachine)
	// ACTIVE only: this is an operator board, and a cleared row must not push a live
	// critical off a 25-row page. The raise-then-clear story shows on the counts and cards.
	alarms.Options["state"] = AlarmStateActiveWire

	widgets := []dashboardWidget{
		buildAlarmCountWidget("sp-alarms-critical", box{0, 6, 0, 3}, onSite, "Critical alarms", AlarmSeverityCriticalWire),
		buildAlarmCountWidget("sp-alarms-major", box{6, 6, 0, 3}, onSite, "Major alarms", AlarmSeverityMajorWire),
		buildLatestCardWidget("sp-plant-throughput", box{12, 6, 0, 3}, onPlant(SitepulseThroughputKey),
			"Crusher throughput", SitepulseThroughputKey, "t/h", 0, false),
		running,
	}

	// Card bands. Each kind is capped at one row; the label says when that is partial.
	shown, perKind := 0, 0
	type band struct {
		devices []DeviceInstance
		row     int
	}
	var fuelBands []band
	for i, kind := range sitepulseMachineKinds {
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
		fuelBands = append(fuelBands, band{devices: all[:n], row: 4 + 2*i})
	}

	widgets = append(widgets, buildLabelWidget("sp-fuel-label", box{0, 24, 3, 1}, sitepulseFuelBandText(shown, perKind), "left", 14))

	for _, b := range fuelBands {
		for i, d := range b.devices {
			slots[d.Token] = sitepulseDeviceSlot(d.ExternalId, d, nil)
			// No flash: a draining tank would flash red on every tick.
			widgets = append(widgets, buildLatestCardWidget("sp-fuel-"+d.Token, box{sitepulseCardColSpan * i, sitepulseCardColSpan, b.row, 2},
				widgetSubject{Slot: d.Token, Measurements: []string{SitepulseFuelKey}},
				d.ExternalId, SitepulseFuelKey, "%", 0, false))
		}
	}

	haulers := sitepulseMachinesOfType(devices, SitepulseHaulerTypeToken)
	if len(haulers) > sitepulseCardsPerKind {
		haulers = haulers[:sitepulseCardsPerKind]
	}
	for i, d := range haulers {
		slots[d.Token] = sitepulseDeviceSlot(d.ExternalId, d, nil)
		widgets = append(widgets, buildLatestCardWidget("sp-payload-"+d.Token, box{sitepulseCardColSpan * i, sitepulseCardColSpan, 10, 2},
			widgetSubject{Slot: d.Token, Measurements: []string{SitepulsePayloadKey}},
			d.ExternalId+" payload", SitepulsePayloadKey, "t", 1, false))
	}

	widgets = append(widgets,
		alarms,
		buildMapWidget("sp-map", box{12, 6, 12, 8},
			widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{}, Location: "latest"}, "Machine positions"),
		buildEntitySelectorWidget("sp-machine-select", box{18, 6, 12, 2}, "Machine", sitepulseSlotMachine),
		buildGaugeWidget("sp-engine-temp", box{18, 6, 14, 3}, onMachine(SitepulseEngineTempKey),
			fmt.Sprintf("Engine temp — overheat above %g °C", SitepulseOverheatThreshold),
			SitepulseEngineTempKey, "°C", 0, sitepulseEngineTempGaugeMax),
		buildGaugeWidget("sp-tyre", box{18, 6, 17, 3}, onMachine(SitepulseTyreKey),
			fmt.Sprintf("Tyre pressure — low below %g kPa", SitepulseTyrePressureThreshold),
			SitepulseTyreKey, "kPa", 0, sitepulseTyreGaugeMax),
	)

	def := dashboardDefinition{
		SchemaVersion: 1,
		Title:         sitepulseDashboardName,
		Canvas:        widgetlabCanvas(),
		Slots:         slots,
		Widgets:       widgets,
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal sitepulse dashboard definition: %w", err)
	}
	return string(raw), nil
}
