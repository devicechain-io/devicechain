// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"time"
)

// The harborpulse boards. Two documents for two people, as towerpulse's are:
//
//   - `hp-dashboard`, the district operator's: a quay picker, the alarms by severity and the
//     devices online on that quay, the Ferry Terminal's demand, the selected gauge's water level
//     against the three flood tiers, every gauge of the quay on one chart, the response truck on a
//     map, the East Quay pump's wet well and the overheating Basin charger's connector (each with
//     its alarms and the commands the platform sent it drawn on), and the alarm table with a tab
//     per kind of equipment.
//   - `hp-lofts-dashboard`, the Quayside Lofts owner's: their building's meter and its alarms,
//     and nothing of the district's. It is built from what that customer may see rather than
//     re-opened from the operator's board (see buildTowerpulseAnnexDashboard for why).
//
// Every band is the rule's own constant, so a card, a chart and an alarm cannot disagree about
// where "flooding" starts.

const (
	HarborpulseDashboardToken      = "hp-dashboard"
	HarborpulseLoftsDashboardToken = "hp-lofts-dashboard"

	harborSlotQuay     = "quay"
	harborSlotGauge    = "gauge"
	harborSlotDistrict = "district"
	harborSlotPump     = "pump"
	harborSlotCharger  = "charger"
	harborSlotFerry    = "ferryMeter"
	harborSlotOwner    = "owner"
	harborSlotMeter    = "meter"

	// An hour of WALL time is two and a half sim days, so the board's default range shows the
	// whole storm with the day before it for comparison.
	harborpulseTimeRangeSeconds = 3600
	// How long a device may be silent before a status card marks it stale: the absence rules'
	// own silence, so the card and the alarm turn together.
	harborpulseStaleSeconds = int(HarborpulseSilence / time.Second)
)

// harborAlarmTabs is the alarm table's tab order, by device type: the flood story first.
//
// Three tabs, not one per type: five tokens' worth of tabs runs out of a phone-width table. The
// lamps' and meters' alarms are under All.
var harborAlarmTabs = []map[string]any{
	{"token": HarborpulseGaugeType},
	{"token": HarborpulsePumpType},
	{"token": HarborpulseChargerType},
}

// harborFloodBands are the three flood tiers, from the rules' own levels.
var harborFloodBands = []widgetThreshold{
	{Value: HarborpulseFloodMinorM, Level: SeverityMinor},
	{Value: HarborpulseFloodMajorM, Level: SeverityMajor},
	{Value: HarborpulseFloodCriticalM, Level: SeverityCritical},
}

func harborAnchor(label, targetType, token string) dashboardSlot {
	return dashboardSlot{
		Type:  "anchor",
		Label: label,
		DefaultBinding: &dashboardSlotBinding{
			Kind:   "anchor",
			Anchor: &dashboardAnchorTarget{Relationship: assignmentRelationshipType, TargetType: targetType, TargetToken: token},
		},
	}
}

func harborDeviceOf(devices []DeviceInstance, token string) (DeviceInstance, error) {
	for _, d := range devices {
		if d.Token == token {
			return d, nil
		}
	}
	return DeviceInstance{}, fmt.Errorf("harborpulse board: no device %q to bind", token)
}

// buildHarborpulseDashboard renders the operator's board, opened on East Quay: the quay that
// floods to CRITICAL, loses its feeder and has the air-locked pump.
func buildHarborpulseDashboard(devices []DeviceInstance) (string, error) {
	named := map[string]DeviceInstance{}
	for _, tok := range []string{"hp-gauge-east-1", harborAirLockPump, harborHotCharger, harborFerryMeter} {
		d, err := harborDeviceOf(devices, tok)
		if err != nil {
			return "", err
		}
		named[tok] = d
	}

	slots := map[string]dashboardSlot{
		harborSlotQuay:     withCandidates(harborAnchor("Quay", "area", HarborpulseEastQuay), HarborpulseQuayTypeToken),
		harborSlotGauge:    sitepulseDeviceSlot("Gauge", named["hp-gauge-east-1"], &dashboardScope{Parent: harborSlotQuay, Strategy: "manual"}),
		harborSlotDistrict: harborAnchor("District", "customer", HarborpulsePublicRealmToken),
		harborSlotPump:     sitepulseDeviceSlot("East Quay pump", named[harborAirLockPump], nil),
		harborSlotCharger:  sitepulseDeviceSlot("Basin charger 2", named[harborHotCharger], nil),
		harborSlotFerry:    sitepulseDeviceSlot("Ferry Terminal meter", named[harborFerryMeter], nil),
	}
	onQuay := widgetSubject{Slot: harborSlotQuay, Measurements: []string{}}
	on := func(slot string, metrics ...string) widgetSubject {
		return widgetSubject{Slot: slot, Measurements: metrics}
	}

	ferry := buildStatCardWidget("hp-ferry-kw", box{6, 3, 1, 3}, on(harborSlotFerry, HarborpulseMeterKwKey),
		"Ferry Terminal demand", HarborpulseMeterKwKey, "last", "kW", 0, 0,
		[]widgetThreshold{{Value: 0, Level: "normal"}, {Value: HarborpulsePeakDemandKw, Level: SeverityMinor}})
	ferry.Options["sparkline"] = true

	level := buildGaugeWidget("hp-gauge-level", box{9, 3, 1, 3}, on(harborSlotGauge, HarborpulseWaterLevelKey),
		"Water level", HarborpulseWaterLevelKey, "m", 0, 3)
	level = withThresholds(level,
		dashboardThreshold{Value: 0, Level: "normal"},
		dashboardThreshold{Value: HarborpulseFloodMinorM, Level: SeverityMinor},
		dashboardThreshold{Value: HarborpulseFloodMajorM, Level: SeverityMajor},
		dashboardThreshold{Value: HarborpulseFloodCriticalM, Level: SeverityCritical})

	// Every gauge of the quay on one chart, its flood alarms drawn on it: on East Quay the lines
	// cross all three bands, elsewhere they stop below the critical one.
	gauges := buildTimeSeriesChartWidget("hp-quay-levels", box{0, 7, 4, 4}, on(harborSlotQuay, HarborpulseWaterLevelKey), "Water levels on the quay")
	delete(gauges.Options, "windowSeconds")
	gauges.Options["groupBy"] = "device"
	gauges.Options["measurement"] = HarborpulseWaterLevelKey
	gauges.Options["maxSeries"] = 6
	gauges.Options["markAlarms"] = true
	gauges.Options["unit"] = "m"
	gauges.Options["min"] = 0.0
	gauges.Options["max"] = 3.0
	gauges.Options["thresholds"] = harborFloodBands

	truck := buildMapWidget("hp-truck-map", box{7, 5, 4, 4},
		widgetSubject{Slot: harborSlotDistrict, Measurements: []string{}, Location: "latest"}, "Response truck")

	// The wet well against the rule's level, with the alarms as spans and the start-pump commands
	// as ticks: the well turns where a tick lands.
	well := buildTimeSeriesChartWidget("hp-pump-well", box{0, 6, 8, 4}, on(harborSlotPump, HarborpulseWetWellKey), "East Quay wet well")
	delete(well.Options, "windowSeconds")
	well.Options["markAlarms"] = true
	well.Options["markCommands"] = true
	well.Options["unit"] = "m"
	well.Options["min"] = 0.0
	well.Options["max"] = harborWellCeiling
	well.Options["thresholds"] = []widgetThreshold{{Value: HarborpulseWetWellM, Level: SeverityMajor}}

	connector := buildTimeSeriesChartWidget("hp-charger-connector", box{6, 6, 8, 4}, on(harborSlotCharger, HarborpulseConnectorKey), "Basin charger 2 connector")
	delete(connector.Options, "windowSeconds")
	connector.Options["markAlarms"] = true
	connector.Options["markCommands"] = true
	connector.Options["unit"] = "°C"
	connector.Options["min"] = 0.0
	connector.Options["max"] = 100.0
	connector.Options["thresholds"] = []widgetThreshold{{Value: HarborpulseConnectorHotC, Level: SeverityMajor}}

	alarms := buildAlarmTableWidget("hp-alarms", box{0, 12, 12, 4}, onQuay, "Alarms on the quay", harborSlotGauge)
	alarms.Options["state"] = AlarmStateActiveWire
	alarms.Options["groupBy"] = "deviceType"
	alarms.Options["deviceTypes"] = harborAlarmTabs

	widgets := []dashboardWidget{
		buildEntitySelectorWidget("hp-quay-select", box{0, 4, 0, 1}, "Quay", harborSlotQuay),
		buildEntitySelectorWidget("hp-gauge-select", box{4, 4, 0, 1}, "Gauge", harborSlotGauge),

		buildAlarmSeverityWidget("hp-alarm-severity", box{0, 3, 1, 3}, onQuay, "Active alarms by severity", ""),
		buildOnlineDoughnutWidget("hp-online", box{3, 3, 1, 3}, onQuay, "Devices online"),
		ferry,
		level,

		gauges,
		truck,

		well,
		connector,

		alarms,
	}
	return marshalHarborBoard("Harborpulse", slots, widgets)
}

// buildHarborpulseLoftsDashboard renders the Quayside Lofts owner's board: their meter and its
// alarms, read-only.
func buildHarborpulseLoftsDashboard(devices []DeviceInstance) (string, error) {
	meter, err := harborDeviceOf(devices, harborLoftsMeter)
	if err != nil {
		return "", err
	}
	slots := map[string]dashboardSlot{
		harborSlotOwner: harborAnchor("Building", "customer", HarborpulseLoftsToken),
		harborSlotMeter: sitepulseDeviceSlot("Meter", meter, nil),
	}
	onOwner := widgetSubject{Slot: harborSlotOwner, Measurements: []string{}}
	on := func(metrics ...string) widgetSubject {
		return widgetSubject{Slot: harborSlotMeter, Measurements: metrics}
	}

	now := buildStatCardWidget("hp-lofts-kw", box{0, 3, 0, 3}, on(HarborpulseMeterKwKey),
		"Demand now", HarborpulseMeterKwKey, "last", "kW", 0, 0, nil)
	now.Options["sparkline"] = true
	peak := buildStatCardWidget("hp-lofts-kw-max", box{3, 3, 0, 3}, on(HarborpulseMeterKwKey),
		"Peak demand", HarborpulseMeterKwKey, "max", "kW", 0, 0, nil)

	trend := buildTimeSeriesChartWidget("hp-lofts-trend", box{0, 9, 3, 4}, on(HarborpulseMeterKwKey), "Demand")
	delete(trend.Options, "windowSeconds")
	trend.Options["unit"] = "kW"

	alarms := buildAlarmTableWidget("hp-lofts-alarms", box{0, 12, 7, 4}, onOwner, "Alarms", harborSlotMeter)
	alarms.Options["state"] = AlarmStateActiveWire

	history := buildHistoryTableWidget("hp-lofts-history", box{0, 12, 11, 5}, on(),
		"Meter history",
		[]map[string]any{
			{"measurement": HarborpulseMeterKwKey, "label": "Demand", "unit": "kW", "precision": 1},
			{"measurement": HarborpulseMeterKwhKey, "label": "Energy", "unit": "kWh", "precision": 0},
		}, 10)

	widgets := []dashboardWidget{
		now,
		peak,
		buildStatCardWidget("hp-lofts-kwh", box{6, 3, 0, 3}, on(HarborpulseMeterKwhKey),
			"Energy", HarborpulseMeterKwhKey, "last", "kWh", 0, 0, nil),
		buildDeviceStatusWidget("hp-lofts-status", box{9, 3, 0, 3}, widgetSubject{Slot: harborSlotMeter}, "Meter status", harborpulseStaleSeconds),
		trend,
		buildAlarmSeverityWidget("hp-lofts-severity", box{9, 3, 3, 3}, onOwner, "Active alarms", ""),
		alarms,
		history,
	}
	return marshalHarborBoard("Quayside Lofts", slots, widgets)
}

func marshalHarborBoard(title string, slots map[string]dashboardSlot, widgets []dashboardWidget) (string, error) {
	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         title,
		Canvas:        widgetlabCanvas(),
		Slots:         slots,
		Widgets:       widgets,
		TimeRange:     &dashboardTimeRange{DefaultSeconds: harborpulseTimeRangeSeconds},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal %s dashboard definition: %w", title, err)
	}
	return string(raw), nil
}

// harborpulseDashboards builds both boards from the manifest's devices.
func harborpulseDashboards(devices []DeviceInstance) []DashboardSpec {
	operator, err := buildHarborpulseDashboard(devices)
	if err != nil {
		panic(fmt.Sprintf("harborpulse: build dashboard definition: %v", err))
	}
	lofts, err := buildHarborpulseLoftsDashboard(devices)
	if err != nil {
		panic(fmt.Sprintf("harborpulse: build lofts dashboard definition: %v", err))
	}
	return []DashboardSpec{
		{
			Token:       HarborpulseDashboardToken,
			Name:        "Harborpulse",
			Description: "The waterfront district: alarms and devices online by quay, flood levels against the three tiers, the response truck, the East Quay pump's wet well, the Basin charger's connector and the alarm table.",
			Definition:  operator,
		},
		{
			Token:       HarborpulseLoftsDashboardToken,
			Name:        "Quayside Lofts",
			Description: "The Quayside Lofts owner board: the building's demand, energy, meter status and alarms, read-only.",
			Definition:  lofts,
			// A member limited to the Lofts lists only what is shared with it.
			SharedWithCustomers: []string{HarborpulseLoftsToken},
		},
	}
}
