// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// buildTowerpulseAnnexDashboard renders the annex owner's board, `tp-annex-dashboard`: the annex
// building, its floors and zones, its chiller and its meter, and nothing that belongs to the tower or
// acts on a device.
//
// 🔴 A CUSTOMER BOARD IS BUILT FROM WHAT THE CUSTOMER MAY HAVE, NOT FROM THE OPERATOR'S BOARD MINUS
// WHAT THE SERVER REFUSES. Data-plane scoping does keep the tower's numbers from an annex owner, but a
// definition that still names the tower chiller, the tower meter and a setpoint control opens as a
// board of empty cards and a stepper that cannot act: it shows nothing about "only the annex" and
// leaves a write affordance on a read-only member's screen. The facility operator's cross-campus board
// (buildTowerpulseDashboard) is a different document for a different person.
//
// Compared with the operator's board it drops the tower chiller and meter cards, the tower power
// history and the setpoint stepper; the annex power history replaces the tower's. The Building and
// Floor pickers carry a type filter (a Floor picker that offered the building or the plant would be
// a wrong answer), and a customer-scoped member sees only the areas their own devices are in, so the
// pickers list the annex alone.
func buildTowerpulseAnnexDashboard(devices []DeviceInstance) (string, error) {
	building := TowerpulseAnnexToken
	floor := towerpulseFloorToken(towerpulseAnnex, 1)
	zoneDevice, err := towerpulseDevice(devices, "tp-vav-annex-f1-north")
	if err != nil {
		return "", err
	}
	chiller, err := towerpulseDevice(devices, "tp-chiller-annex")
	if err != nil {
		return "", err
	}
	meter, err := towerpulseDevice(devices, "tp-meter-annex")
	if err != nil {
		return "", err
	}

	slots := map[string]dashboardSlot{
		towerpulseSlotBuilding:     withCandidates(towerpulseAnchor("Building", building), TowerpulseBuildingTypeToken),
		towerpulseSlotFloor:        withCandidates(towerpulseAnchor("Floor", floor), TowerpulseFloorTypeToken),
		towerpulseSlotZone:         sitepulseDeviceSlot("Zone", zoneDevice, &dashboardScope{Parent: towerpulseSlotBuilding, Strategy: "manual"}),
		towerpulseSlotChillerAnnex: sitepulseDeviceSlot("Annex chiller", chiller, nil),
		towerpulseSlotMeterAnnex:   sitepulseDeviceSlot("Annex meter", meter, nil),
	}
	onBuilding := widgetSubject{Slot: towerpulseSlotBuilding, Measurements: []string{}}
	on := func(slot string, metrics ...string) widgetSubject {
		return widgetSubject{Slot: slot, Measurements: metrics}
	}

	kw := buildStatCardWidget("tp-kw-annex", box{6, 3, 1, 3}, on(towerpulseSlotMeterAnnex, TowerpulseMeterKwKey),
		"Annex power", TowerpulseMeterKwKey, "last", "kW", 0, 0, nil)
	kw.Options["sparkline"] = true

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

	zoneTemp := withThresholds(buildGaugeWidget("tp-zone-temp", box{4, 4, 4, 3}, on(towerpulseSlotZone, TowerpulseTempKey),
		"Temperature", TowerpulseTempKey, "°C", 16, 34),
		dashboardThreshold{Value: 16, Level: "normal"}, dashboardThreshold{Value: TowerpulseZoneHotCelsius, Level: TowerpulseZoneHotSeverity})
	zoneCO2 := withThresholds(buildGaugeWidget("tp-zone-co2", box{8, 4, 4, 3}, on(towerpulseSlotZone, TowerpulseCO2Key),
		"CO2", TowerpulseCO2Key, "ppm", 400, 1400),
		dashboardThreshold{Value: 400, Level: "normal"}, dashboardThreshold{Value: TowerpulseCO2Ppm, Level: TowerpulseCO2Severity})

	history := buildHistoryTableWidget("tp-power-history", box{0, 12, 15, 5}, on(towerpulseSlotMeterAnnex),
		"Annex power history",
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
		kw,
		buildStatCardWidget("tp-kwh-m2-annex", box{9, 3, 1, 3}, on(towerpulseSlotMeterAnnex, TowerpulseKwhPerM2Key),
			"Annex energy per m2", TowerpulseKwhPerM2Key, "last", "kWh/m2", 2, 0, nil),

		buildDeviceStatusWidget("tp-chiller-annex-status", box{0, 4, 4, 3}, widgetSubject{Slot: towerpulseSlotChillerAnnex}, "Annex chiller status", towerpulseStaleSeconds),
		zoneTemp,
		zoneCO2,

		compare,
		cop,
		alarms,
		history,
	}

	def := dashboardDefinition{
		SchemaVersion: dashboardSchemaVersion,
		Title:         "Towerpulse annex",
		Canvas:        widgetlabCanvas(),
		Slots:         slots,
		Widgets:       widgets,
		TimeRange:     &dashboardTimeRange{DefaultSeconds: towerpulseTimeRangeSeconds},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal towerpulse annex dashboard definition: %w", err)
	}
	return string(raw), nil
}
