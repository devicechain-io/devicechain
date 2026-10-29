// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// towerpulseSlotAnnex is the annex board's root: the annex owner's CUSTOMER, not an area.
const towerpulseSlotAnnex = "annex"

// buildTowerpulseAnnexDashboard renders the annex owner's board, `tp-annex-dashboard`: the annex's
// devices, its alarms, its chiller and its meter, and nothing that belongs to the tower or acts on a
// device.
//
// 🔴 A CUSTOMER BOARD IS BUILT FROM WHAT THE CUSTOMER MAY HAVE, NOT FROM THE OPERATOR'S BOARD MINUS
// WHAT THE SERVER REFUSES. Data-plane scoping does keep the tower's numbers from an annex owner, but a
// definition that still names the tower chiller, the tower meter and a setpoint control opens as a
// board of empty cards and a stepper that cannot act: it shows nothing about "only the annex" and
// leaves a write affordance on a read-only member's screen. The facility operator's cross-campus board
// (buildTowerpulseDashboard) is a different document for a different person.
//
// 🔴 NO AREA ANCHORS. A member limited to a customer cannot read the relationship graph or list areas
// (both answer FORBIDDEN, reason SCOPE_UNSUPPORTED), so a board anchored on the annex building and
// floor opened for them with its context dropped, its Floor and Zone pickers empty and every aggregate
// disabled. This board roots on the annex owner's CUSTOMER with the `assigned` relationship, which
// customerDevices answers for exactly that member, and binds the rest to devices: the aggregates
// (alarms by severity, devices online, the alarm table, the zone comparison) read the customer's
// devices, and the one picker offers those same devices. It is the binding the customer-users guide
// asks of a shared board. Floors are an area concept and have no counterpart here.
func buildTowerpulseAnnexDashboard(devices []DeviceInstance) (string, error) {
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
		towerpulseSlotAnnex: {
			Type:  "anchor",
			Label: "Annex",
			DefaultBinding: &dashboardSlotBinding{
				Kind:   "anchor",
				Anchor: &dashboardAnchorTarget{Relationship: assignmentRelationshipType, TargetType: "customer", TargetToken: TowerpulseAnnexOwnerToken},
			},
		},
		towerpulseSlotZone:         sitepulseDeviceSlot("Device", zoneDevice, &dashboardScope{Parent: towerpulseSlotAnnex, Strategy: "manual"}),
		towerpulseSlotChillerAnnex: sitepulseDeviceSlot("Annex chiller", chiller, nil),
		towerpulseSlotMeterAnnex:   sitepulseDeviceSlot("Annex meter", meter, nil),
	}
	onAnnex := widgetSubject{Slot: towerpulseSlotAnnex, Measurements: []string{}}
	on := func(slot string, metrics ...string) widgetSubject {
		return widgetSubject{Slot: slot, Measurements: metrics}
	}

	kw := buildStatCardWidget("tp-kw-annex", box{6, 3, 1, 3}, on(towerpulseSlotMeterAnnex, TowerpulseMeterKwKey),
		"Annex power", TowerpulseMeterKwKey, "last", "kW", 0, 0, nil)
	kw.Options["sparkline"] = true

	compare := buildTimeSeriesChartWidget("tp-floor-compare", box{0, 7, 7, 4}, on(towerpulseSlotAnnex, TowerpulseTempKey), "Zone temperatures")
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

	alarms := buildAlarmTableWidget("tp-alarms", box{0, 12, 11, 4}, onAnnex, "Alarms", towerpulseSlotZone)
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
		buildEntitySelectorWidget("tp-zone-select", box{0, 12, 0, 1}, "Device", towerpulseSlotZone),

		buildAlarmSeverityWidget("tp-alarm-severity", box{0, 3, 1, 3}, onAnnex, "Active alarms by severity", ""),
		buildOnlineDoughnutWidget("tp-online", box{3, 3, 1, 3}, onAnnex, "Devices online"),
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
