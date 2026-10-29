// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"time"
)

// The towerpulse operator board, `tp-dashboard`: the facility operator's view of both buildings.
// The annex owner has a board of their own (towerpulse_annex_dashboard.go), built from only what
// that customer may see rather than re-opened from this one.
//
// What is on it, top to bottom, and what each widget proves:
//
//	row 0       the three SELECTORS: building and floor (area anchors) and zone (a device scoped to
//	            the building, so picking another building keeps the zone only while it still belongs)
//	rows 1-3    alarms by severity and devices online for the building, and the power of each
//	            building with its trend (a stat card with a sparkline)
//	rows 4-6    each chiller's status card, and each building's energy per floor area, which is a
//	            CALCULATED metric (energy over a device constant)
//	rows 7-10   the floor's zones compared on one chart with their alarms marked, and the annex
//	            chiller's coefficient of performance, also calculated
//	rows 11-14  the alarm table, with a tab per kind of equipment
//	rows 15-19  the setpoint control (a stepper that sends set-setpoint to the selected zone), the
//	            selected zone's temperature and CO2, and the tower's power history, a table the
//	            widget frame can download as CSV
//
// The time range is the board's own header control: an hour by default, and the viewer can
// move it to an absolute window. Nothing about it is authored beyond the default.
//
// 🔴 THE CHILLERS AND METERS ARE BOUND BY NAME, one slot each, not through the building
// selector. A scoped slot lists the building's members of every kind, so a chiller card scoped to
// the building would offer the zones too. Two fixed cards per figure also put the story on the
// screen at once: the annex chiller going quiet is visible beside a healthy tower chiller.
//
// 🔴 THE AREA SELECTORS LIST EVERY AREA, floors and plants included. The platform has no area
// hierarchy to filter a building list by, and the selector offers whatever areas the tenant
// has. Picking a floor gives a floor board (everything on it), which is a use rather than a
// fault, but a reader who expects only "Tower" and "Annex" should know the list is longer.

const (
	towerpulseSlotBuilding     = "building"
	towerpulseSlotFloor        = "floor"
	towerpulseSlotZone         = "selectedZone"
	towerpulseSlotChillerTower = "chillerTower"
	towerpulseSlotChillerAnnex = "chillerAnnex"
	towerpulseSlotMeterTower   = "meterTower"
	towerpulseSlotMeterAnnex   = "meterAnnex"

	towerpulseTimeRangeSeconds = 3600
	// How long a chiller may be silent before its status card marks it stale: the rule's own
	// silence, so the card and the alarm turn at the same moment.
	towerpulseStaleSeconds = int(TowerpulseChillerSilence / time.Second)
)

// towerpulseAlarmTabs is the alarm table's tab order, by device type.
var towerpulseAlarmTabs = []map[string]any{
	{"token": TowerpulseVAVTypeToken},
	{"token": TowerpulseAHUTypeToken},
	{"token": TowerpulseChillerTypeToken},
	{"token": TowerpulseMeterTypeToken},
}

func towerpulseAnchor(label, areaToken string) dashboardSlot {
	return dashboardSlot{
		Type:  "anchor",
		Label: label,
		DefaultBinding: &dashboardSlotBinding{
			Kind:   "anchor",
			Anchor: &dashboardAnchorTarget{Relationship: assignmentRelationshipType, TargetType: "area", TargetToken: areaToken},
		},
	}
}

func towerpulseDevice(devices []DeviceInstance, token string) (DeviceInstance, error) {
	for _, d := range devices {
		if d.Token == token {
			return d, nil
		}
	}
	return DeviceInstance{}, fmt.Errorf("towerpulse board: no device %q to bind", token)
}

// buildTowerpulseDashboard renders the operator's board from the campus's devices, opened on the
// tower.
func buildTowerpulseDashboard(devices []DeviceInstance) (string, error) {
	building, floor, zone := TowerpulseTowerToken, towerpulseFloorToken(towerpulseTower, 4), TowerpulseHotZone
	zoneDevice, err := towerpulseDevice(devices, zone)
	if err != nil {
		return "", err
	}
	named := map[string]DeviceInstance{}
	for _, tok := range []string{"tp-chiller-tower", "tp-chiller-annex", "tp-meter-tower", "tp-meter-annex"} {
		d, err := towerpulseDevice(devices, tok)
		if err != nil {
			return "", err
		}
		named[tok] = d
	}

	slots := map[string]dashboardSlot{
		towerpulseSlotBuilding:     towerpulseAnchor("Building", building),
		towerpulseSlotFloor:        towerpulseAnchor("Floor", floor),
		towerpulseSlotZone:         sitepulseDeviceSlot("Zone", zoneDevice, &dashboardScope{Parent: towerpulseSlotBuilding, Strategy: "manual"}),
		towerpulseSlotChillerTower: sitepulseDeviceSlot("Tower chiller", named["tp-chiller-tower"], nil),
		towerpulseSlotChillerAnnex: sitepulseDeviceSlot("Annex chiller", named["tp-chiller-annex"], nil),
		towerpulseSlotMeterTower:   sitepulseDeviceSlot("Tower meter", named["tp-meter-tower"], nil),
		towerpulseSlotMeterAnnex:   sitepulseDeviceSlot("Annex meter", named["tp-meter-annex"], nil),
	}

	onBuilding := widgetSubject{Slot: towerpulseSlotBuilding, Measurements: []string{}}
	on := func(slot string, metrics ...string) widgetSubject {
		return widgetSubject{Slot: slot, Measurements: metrics}
	}

	// Every band below is built from the rules' own constants, so a card, a chart and an alarm
	// cannot disagree about where "too hot" starts.

	kwTower := buildStatCardWidget("tp-kw-tower", box{6, 3, 1, 3}, on(towerpulseSlotMeterTower, TowerpulseMeterKwKey),
		"Tower power", TowerpulseMeterKwKey, "last", "kW", 0, 0, nil)
	kwTower.Options["sparkline"] = true
	kwAnnex := buildStatCardWidget("tp-kw-annex", box{9, 3, 1, 3}, on(towerpulseSlotMeterAnnex, TowerpulseMeterKwKey),
		"Annex power", TowerpulseMeterKwKey, "last", "kW", 0, 0, nil)
	kwAnnex.Options["sparkline"] = true

	// The floor's zones on one chart, one line each, with the alarms drawn on it. The board's
	// range applies (no window of its own), and the axis is framed around a room's working
	// range so a line at 22 C is not a flat stroke at the bottom of a 0..40 axis.
	compare := buildTimeSeriesChartWidget("tp-floor-compare", box{0, 7, 7, 4}, on(towerpulseSlotFloor, TowerpulseTempKey), "Zones on the floor")
	delete(compare.Options, "windowSeconds")
	compare.Options["groupBy"] = "device"
	compare.Options["measurement"] = TowerpulseTempKey
	compare.Options["maxSeries"] = 6
	compare.Options["markAlarms"] = true
	compare.Options["unit"] = "°C"
	compare.Options["min"] = 18.0
	compare.Options["max"] = 32.0
	compare.Options["thresholds"] = []widgetThreshold{{Value: TowerpulseZoneHotCelsius, Level: TowerpulseZoneHotSeverity}}

	cop := buildTimeSeriesChartWidget("tp-annex-cop", box{7, 5, 7, 4}, on(towerpulseSlotChillerAnnex, TowerpulseCOPKey), "Annex chiller COP")
	delete(cop.Options, "windowSeconds")
	cop.Options["precision"] = 2

	alarms := buildAlarmTableWidget("tp-alarms", box{0, 12, 11, 4}, onBuilding, "Alarms", towerpulseSlotZone)
	alarms.Options["state"] = AlarmStateActiveWire
	alarms.Options["groupBy"] = "deviceType"
	alarms.Options["deviceTypes"] = towerpulseAlarmTabs

	// A stepper, not a slider: a setpoint moves by a notch at a time. The range is the
	// command's own (the control reads it from the baked schema); only the step is authored.
	stepper := buildCommandSliderWidget("tp-setpoint", box{0, 6, 15, 2}, widgetSubject{Slot: towerpulseSlotZone},
		"Setpoint", TowerpulseSetpointCommandKey, "Set setpoint", towerpulseSetpointParameterSchema, TowerpulseSetpointParameter, 0.5)
	stepper.Options["mode"] = "stepper"

	zoneTemp := withThresholds(buildGaugeWidget("tp-zone-temp", box{0, 3, 17, 3}, on(towerpulseSlotZone, TowerpulseTempKey),
		"Zone temperature", TowerpulseTempKey, "°C", 16, 34),
		dashboardThreshold{Value: 16, Level: "normal"}, dashboardThreshold{Value: TowerpulseZoneHotCelsius, Level: TowerpulseZoneHotSeverity})
	zoneCO2 := withThresholds(buildGaugeWidget("tp-zone-co2", box{3, 3, 17, 3}, on(towerpulseSlotZone, TowerpulseCO2Key),
		"Zone CO2", TowerpulseCO2Key, "ppm", 400, 1400),
		dashboardThreshold{Value: 400, Level: "normal"}, dashboardThreshold{Value: TowerpulseCO2Ppm, Level: TowerpulseCO2Severity})

	history := buildHistoryTableWidget("tp-power-history", box{6, 6, 15, 5}, on(towerpulseSlotMeterTower),
		"Tower power history",
		[]map[string]any{
			{"measurement": TowerpulseMeterKwKey, "label": "Power", "unit": "kW", "precision": 1},
			{"measurement": TowerpulseMeterKwhKey, "label": "Energy", "unit": "kWh", "precision": 0},
			{"measurement": TowerpulseKwhPerM2Key, "label": "Energy per m2", "unit": "kWh/m2", "precision": 2},
		}, 10)

	widgets := []dashboardWidget{
		buildEntitySelectorWidget("tp-building-select", box{0, 4, 0, 1}, "Building", towerpulseSlotBuilding),
		buildEntitySelectorWidget("tp-floor-select", box{4, 4, 0, 1}, "Floor", towerpulseSlotFloor),
		buildEntitySelectorWidget("tp-zone-select", box{8, 4, 0, 1}, "Zone", towerpulseSlotZone),

		buildAlarmSeverityWidget("tp-alarm-severity", box{0, 3, 1, 3}, onBuilding, "Active alarms by severity", ""),
		buildOnlineDoughnutWidget("tp-online", box{3, 3, 1, 3}, onBuilding, "Devices online"),
		kwTower,
		kwAnnex,

		buildDeviceStatusWidget("tp-chiller-tower-status", box{0, 3, 4, 3}, widgetSubject{Slot: towerpulseSlotChillerTower}, "Tower chiller status", towerpulseStaleSeconds),
		buildDeviceStatusWidget("tp-chiller-annex-status", box{3, 3, 4, 3}, widgetSubject{Slot: towerpulseSlotChillerAnnex}, "Annex chiller status", towerpulseStaleSeconds),
		buildStatCardWidget("tp-kwh-m2-tower", box{6, 3, 4, 3}, on(towerpulseSlotMeterTower, TowerpulseKwhPerM2Key),
			"Tower energy per m2", TowerpulseKwhPerM2Key, "last", "kWh/m2", 2, 0, nil),
		buildStatCardWidget("tp-kwh-m2-annex", box{9, 3, 4, 3}, on(towerpulseSlotMeterAnnex, TowerpulseKwhPerM2Key),
			"Annex energy per m2", TowerpulseKwhPerM2Key, "last", "kWh/m2", 2, 0, nil),

		compare,
		cop,
		alarms,
		stepper,
		zoneTemp,
		zoneCO2,
		history,
	}

	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         "Towerpulse",
		Canvas:        widgetlabCanvas(),
		Slots:         slots,
		Widgets:       widgets,
		TimeRange:     &dashboardTimeRange{DefaultSeconds: towerpulseTimeRangeSeconds},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal towerpulse dashboard definition: %w", err)
	}
	return string(raw), nil
}
