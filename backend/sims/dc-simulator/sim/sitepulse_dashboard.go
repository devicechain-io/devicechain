// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// The sitepulse board, `sp-dashboard`: the site's active alarms, the crusher plant, machine
// positions and ONE table of the whole fleet, laid out to sit beside the 3D window the Unity
// player renders.
//
// 🔴 THE BOARD ASSUMES ~960 px OF WIDTH (half a 1920 screen, the other half being the 3D
// window) AND 1080 px OF HEIGHT, AND IS DRAWN TO FIT THEM WITHOUT SCROLLING THE PAGE: every
// widget ends at or above the fold (sitepulseFoldRows), the fleet table included. It is laid out
// in SQUARE cells on the 12-column grid: at 960 px a cell is ~73 px, comfortably above the 56 px
// minimum, so the board renders as authored there and every widget is inside the size its
// descriptor declares, which the frontend layout lint enforces over the committed fixture. On a
// full 1920 px screen the cells double and every widget only grows. From the top:
//
//	rows 0-4    nine columns of KPIs: the alarm counts, the crusher lamp and the plant's peak
//	            throughput (two rows), then its throughput now (with its trend) and on average,
//	            and how many machines are low on fuel (three rows); beside them the Machine
//	            selector and the engine gauge
//	rows 5-7    the alarm table (a tab per kind of machine), the map (a marker click selects the
//	            machine) and the tyre gauge
//	rows 8-12   the fleet table
//
// 🔴 THE FLEET IS ONE TABLE, NOT A TANK PER MACHINE. The fleet-table widget draws one row per
// device of the site anchor and one column per measurement, each cell that DEVICE's own latest
// reading, so eighteen machines read as eighteen rows instead of eighteen cards. (A widget that
// keys its readings by measurement NAME would collapse a fleet into one value, which is why the
// board used to spend a level gauge on every machine.) The columns are fuel, engine temperature,
// payload and tyre pressure, each a measurement the machines REALLY report, and each column that
// a rule watches carries that rule's bands, built from the same constants the rules and the two
// gauges use, so the table, the gauges and the alarms cannot disagree. A cell for a measurement
// a machine never reports says so ("does not report this") rather than showing a zero: a dozer
// is tracked and has no tyre pressure or load, and the crusher plant reports none of the four.
// The table is sorted lowest fuel first, which is the order a site manager acts in, and the
// device name drills to the selected-machine slot exactly as an alarm's originator does.
//
// 🔴 A ROW IS ONE LINE, AND THE TABLE STILL CANNOT SHOW ALL NINETEEN BESIDE THE REST. Every
// machine reports every second or so, so an age under every value would read "a few seconds ago"
// nineteen times over (and "8 days ago" in the played-back run) and double each row. The table
// sets `ageLagSeconds` instead: a cell shows its age only when its reading has fallen
// sitepulseFleetAgeLagSeconds behind the newest reading in the table, which is a machine that
// went quiet, in the live board and the replay alike, and the frame's footer carries the fleet's
// own age. Even at one line (~25 px) the 19 rows need ~7 of the screen's 13 rows; the table gets
// the last five, shows the first twelve at 960 px and scrolls the rest inside itself (one
// page holds all 19). That is why the order is lowest fuel first: the rows in view are the ones
// that need a person.
//
// Payload is a COLUMN, not a chart of its own: a bar per hauler would repeat the table's figure
// in a second place, spend rows on it, and show fewer machines than the column does. The wheeled
// machines that carry a load, the haulers and the loaders, report it (a loader's is its
// bucket). No rule watches payload, so the column has no bands.
//
// 🔴 EVERY WIDGET HERE IS BOUND TO A MEASUREMENT THE SIM REALLY REPORTS, and the ones that would
// need an invented number are absent on purpose. The sim reports fuel, engine temperature,
// engine hours and position per machine, payload and tyre pressure per wheeled machine (haulers
// and loaders; a dozer is tracked), and throughput and a running flag for the plant: no radio
// signal and no battery, so no signal-bars or battery widget; no heading, so no compass; and no
// shift target or plant capacity, so no progress-ring (the throughput stat trio stands in for
// it). Every band edge is a rule threshold, and there is deliberately no "warning" band below
// the overheat edge: no rule warns before it alarms.
//
// Engine temperature and tyre pressure are also served beside the table: the alarm table
// carries each machine's overheat and tyre alarms with the triggering value, the counts total
// them, and two gauges follow the SELECTED machine, which an alarm row's originator drill or a
// fleet-table row's device name re-points. The selection DEFAULTS TO THE FIRST HAUL TRUCK, not
// the first machine: a dozer runs on tracks and reports no tyre pressure (the equipment profile
// declares the metric for every machine, but the player emits it only for the wheeled ones, as
// the first recorded run shows), so a dozer default opened the board on an empty tyre gauge.
//
// 🔴 THE SITE-WIDE SURFACES ARE BOUNDED, AND SAY SO HERE. A scoped alarm widget runs one alarms
// query PER member device (dashboards hub.ts queryAlarms), and an anchor resolves at most one
// page of members (dashboards resolver.ts ANCHOR_PAGE_SIZE, 500). At the S1 size that is 19
// devices and 3 widgets, which is nothing. Under a large `--devices` run the board is both
// expensive and partial past 500 devices: it is the 18-machine demo's board. The fleet table
// pages its rows and says "Showing N of M devices" itself when the anchor is cut, so it needs
// no note of its own. A tenant-wide alarm widget (no datasource) would avoid the fan-out but the
// widget gate requires every alarm widget to bind one.
//
// The Machine selector lists the anchor's members, which include the crusher and the dozers:
// picking the crusher points the two gauges at a device that reports neither metric, and a
// dozer leaves the tyre gauge empty, which the gauge says (a dash, and no needle) rather than
// resting a needle on a number. No anchor covers the machines only, and adding one is out of
// scope: the crusher is therefore the table's last row, every cell "does not report this".
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
	// at 960, and 1080 px holds 13 of them. Every widget must END at or above it. Integer
	// arithmetic, rounded down: a row the window shows only part of is below the fold.
	sitepulseFoldRows = (sitepulseViewportHeight - 2*dashboardGridPadding + dashboardGridGutter) * dashboardGridColumns /
		(sitepulseViewportWidth - 2*dashboardGridPadding + dashboardGridGutter)

	// The board's time range, the span its aggregate cards and charts cover until a viewer picks
	// another: an hour is long enough for a plant average to mean something and short enough to
	// follow a shift's trouble. Stated explicitly (it is also the unauthored default) so the
	// header's range control is part of what this board is declared to carry.
	sitepulseTimeRangeSeconds = 3600

	// Rows. The KPIs lead in two bands across NINE columns. The first, two rows tall, holds the
	// site's state at two columns a tile (the two alarm counts and the crusher lamp, each at its
	// descriptor's minimum width) and the plant's PEAK throughput in the three columns that leaves.
	// The second, THREE rows tall, holds three tiles of three columns: the plant's throughput now
	// and on average, and the low-fuel count. Neither a stat card nor a fleet count can be
	// narrower than three columns, and a fleet count cannot be shorter than three rows, so the
	// count takes the tall band and the peak card the short one: no other arrangement keeps the
	// fleet table its five rows. The peak is the figure that moves least, and at two rows (~150 px
	// at 960) the fitted curve sets it at ~32 px, the size of the alarm counts beside it.
	//
	// The "now" and "average" cards keep the tall band because a stat card spends its height on
	// more than a count does: a subject line, the reading's age and a caption under the figure,
	// and at three rows they are the largest figures on the board, which is what the plant's
	// output should be. The alarm counts carry no age and no caption, so two rows hold a figure
	// that reads at a distance. A third row for the state band would cost the fleet table its
	// fifth row, the difference between nine machines in view and seven.
	//
	// The remaining 3 columns carry the Machine selector and the engine gauge beside the KPIs,
	// and the alarm table, the map and the tyre gauge share the three rows under them, so the
	// fleet table has the five rows left to the fold.
	sitepulseStateBandRows = 2
	sitepulseFlowBandRows  = 3
	sitepulseKPIRows       = sitepulseStateBandRows + sitepulseFlowBandRows
	sitepulseKPITileCols   = 3
	sitepulseStateTileCols = 2
	sitepulseKPIColumns    = 3 * sitepulseKPITileCols
	sitepulseGaugeCols     = dashboardGridColumns - sitepulseKPIColumns
	sitepulseMidRows       = 3
	sitepulseFleetRow      = sitepulseKPIRows + sitepulseMidRows
	sitepulseFleetRows     = sitepulseFoldRows - sitepulseFleetRow

	// The fleet table pages at 20 devices: the 18 machines and the crusher plant fit one page.
	// A longer fleet pages.
	sitepulseFleetPageSize = 20

	// A fleet-table cell shows its reading's age once the reading lags the newest one in the table
	// by this much. The player reports every machine about once a second (the recorded run's
	// readings at any instant sit within a second of each other), so 30 s is some thirty missed
	// reports: a machine that has gone quiet, never one that is merely between two readings.
	sitepulseFleetAgeLagSeconds = 30

	// Display scales for the engine gauge and the tyre gauge. Not contract values: the only
	// fixed points are the rule thresholds, and each scale keeps its threshold well inside the arc.
	sitepulseEngineTempGaugeMax = 130.0
	sitepulseTyreGaugeMax       = 1000.0
)

// sitepulseAlarmTabs is the alarm table's tab order, as the `deviceTypes` option's list items.
var sitepulseAlarmTabs = []map[string]any{
	{"token": SitepulseHaulerTypeToken},
	{"token": SitepulseLoaderTypeToken},
	{"token": SitepulseDozerTypeToken},
	{"token": SitepulsePlantTypeToken},
}

// sitepulseFleetBands turns a rule's bands into a fleet-table column's `thresholds` entries, tied to the measurement it
// colours: the table's entries carry the measurement, unlike a gauge's.
func sitepulseFleetBands(measurement string, bands []widgetThreshold) []map[string]any {
	out := make([]map[string]any, 0, len(bands))
	for _, b := range bands {
		out = append(out, map[string]any{"measurement": measurement, "value": b.Value, "level": b.Level})
	}
	return out
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
	// The gauges' default subject is the first haul truck, which reports both gauges' metrics.
	firstHauler, err := firstOfType(devices, SitepulseHaulerTypeToken)
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
		sitepulseSlotMachine: sitepulseDeviceSlot("Machine", firstHauler, &dashboardScope{Parent: sitepulseSlotSite, Strategy: "manual"}),
	}

	onPlant := func(metric string) widgetSubject {
		return widgetSubject{Slot: sitepulseSlotPlant, Measurements: []string{metric}}
	}
	onMachine := func(metric string) widgetSubject {
		return widgetSubject{Slot: sitepulseSlotMachine, Measurements: []string{metric}}
	}
	onSite := widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{}}

	// stateTile and flowTile place the i-th tile (0..2, left to right) of the two KPI bands. The
	// peak card follows the state band's three tiles, in the columns they leave.
	stateTile := func(i int) box {
		return box{sitepulseStateTileCols * i, sitepulseStateTileCols, 0, sitepulseStateBandRows}
	}
	flowTile := func(i int) box {
		return box{sitepulseKPITileCols * i, sitepulseKPITileCols, sitepulseStateBandRows, sitepulseFlowBandRows}
	}

	// The crusher lamp: lit while plant_running reports 1 (onAt), dark at 0. No threshold bands:
	// no rule fires on the plant, so no band would have a source. No unit either (it is a flag).
	onAt := 1.0
	running := buildStatusLightWidget("sp-plant-running", stateTile(2), onPlant(SitepulsePlantRunningKey),
		"Crusher running", SitepulsePlantRunningKey, "", &onAt, nil)
	running.Options["precision"] = 0
	// An empty unit is no unit: omit the key rather than author a blank one.
	delete(running.Options, "unit")

	alarms := buildAlarmTableWidget("sp-alarms", box{0, 5, sitepulseKPIRows, sitepulseMidRows}, onSite, "Active alarms", sitepulseSlotMachine)
	// ACTIVE only: this is an operator board, and a cleared row must not push a live
	// critical off a 25-row page. The raise-then-clear story shows on the counts and cards.
	alarms.Options["state"] = AlarmStateActiveWire
	// One tab per kind of machine, plus All, in the order a site manager reads the fleet: the
	// haulers that move the material, the loaders that feed them, the dozers that prepare the
	// ground, then the crusher plant. An unlisted type (a future machine kind) is not tabbed,
	// but its alarms still show under All.
	alarms.Options["groupBy"] = "deviceType"
	alarms.Options["deviceTypes"] = sitepulseAlarmTabs

	// The bands of the three rules, each the rule's own constants: the low-fuel rule's severity
	// from empty and normal from the threshold it fires below (a "below X" rule is two entries,
	// because a band runs to the next); the overheat rule's single edge, with NO warning band
	// below it (nothing in the sim warns before the alarm, and an invented one would colour a
	// reading the platform does not call a problem); and the tyre rule's, a "below" rule again.
	lowFuel := []widgetThreshold{
		{Value: 0, Level: SitepulseSeverity},
		{Value: SitepulseLowFuelThreshold, Level: "normal"},
	}
	overheat := []widgetThreshold{
		{Value: 0, Level: "normal"},
		{Value: SitepulseOverheatThreshold, Level: SitepulseOverheatSeverity},
	}
	lowTyre := []widgetThreshold{
		{Value: 0, Level: SitepulseTyreSeverity},
		{Value: SitepulseTyrePressureThreshold, Level: "normal"},
	}

	// The fleet table: a row per device of the site, the four measurements the machines report.
	// One decimal on every banded column: a rounded figure beside a band edge would show "600 kPa"
	// in the alarm colour for a 599.6 reading, or "15 %" for 14.6.
	//
	// Lowest fuel first (ascending): the machine about to run dry is the first row, and a device
	// with no fuel reading (the crusher) always sorts last. The device name drills to the
	// selected-machine slot, so a click moves the engine and tyre gauges like an alarm's does.
	//
	// No `staleAfterSeconds`: a stale mark is judged against the VIEWER's clock, and this board is
	// also PLAYED BACK from a recording (the website) whose readings are all days old, so every
	// cell would be dimmed and badged and the frame would say STALE: a washed-out table reads as a
	// broken one. The board-replay harness does not re-time a run's samples to the present. The
	// lag rule (`ageLagSeconds`, above) is measured between readings instead, so it marks the
	// machine that went quiet in both places and nothing else.
	fleet := buildFleetTableWidget("sp-fleet", box{0, dashboardGridColumns, sitepulseFleetRow, sitepulseFleetRows},
		widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{SitepulseFuelKey, SitepulseEngineTempKey, SitepulsePayloadKey, SitepulseTyreKey}},
		"Fleet",
		[]map[string]any{
			{"measurement": SitepulseFuelKey, "label": "Fuel", "unit": "%", "precision": 1},
			{"measurement": SitepulseEngineTempKey, "label": "Engine", "unit": "°C", "precision": 1},
			{"measurement": SitepulsePayloadKey, "label": "Payload", "unit": "t", "precision": 1},
			{"measurement": SitepulseTyreKey, "label": "Tyres", "unit": "kPa", "precision": 1},
		},
		append(append(sitepulseFleetBands(SitepulseFuelKey, lowFuel), sitepulseFleetBands(SitepulseEngineTempKey, overheat)...),
			sitepulseFleetBands(SitepulseTyreKey, lowTyre)...),
		0, SitepulseFuelKey)
	fleet.Options["sortDescending"] = false
	fleet.Options["selectionTarget"] = sitepulseSlotMachine
	fleet.Options["pageSize"] = sitepulseFleetPageSize
	fleet.Options["ageLagSeconds"] = sitepulseFleetAgeLagSeconds

	// Throughput three ways. "Now" is the newest reading (stat `last`), aged on screen and
	// refused once it is older than the board's range; the average and the peak cover the
	// board's time range (windowSeconds 0) and caption themselves "Average · 1h" from the span
	// in force. No thresholds: no rule fires on throughput, and the sim defines no target or
	// capacity, so there is no honest progress-ring either.
	//
	// The trend sparkline is drawn on "now" alone: it is the one card whose figure moves, and the
	// average beside it would repeat the same line.
	nowCard := buildStatCardWidget("sp-plant-throughput-now", flowTile(0), onPlant(SitepulseThroughputKey),
		"Throughput now", SitepulseThroughputKey, "last", "t/h", 0, 0, nil)
	nowCard.Options["sparkline"] = true

	// How many machines are below the low-fuel edge, from the SAME bands as the fleet table's
	// fuel column, so the count and the red cells cannot disagree. Attention mode counts the
	// devices in an abnormal band ("normal" is neutral), so a machine with no fuel reading (the
	// crusher) is not flagged.
	lowFuelCount := buildFleetCountWidget("sp-low-fuel", flowTile(2),
		widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{SitepulseFuelKey}},
		"Low fuel", "attention", SitepulseFuelKey, lowFuel)

	// A marker click selects the machine, as a table row or an alarm's originator does.
	siteMap := buildMapWidget("sp-map", box{5, 4, sitepulseKPIRows, sitepulseMidRows},
		widgetSubject{Slot: sitepulseSlotSite, Measurements: []string{}, Location: "latest"}, "Machine positions")
	siteMap.Options["selectionTarget"] = sitepulseSlotMachine

	widgets := []dashboardWidget{
		buildAlarmCountWidget("sp-alarms-critical", stateTile(0), onSite, "Critical alarms", AlarmSeverityCriticalWire),
		buildAlarmCountWidget("sp-alarms-major", stateTile(1), onSite, "Major alarms", AlarmSeverityMajorWire),
		running,
		nowCard,
		buildStatCardWidget("sp-plant-throughput-avg", flowTile(1), onPlant(SitepulseThroughputKey),
			"Avg throughput", SitepulseThroughputKey, "avg", "t/h", 0, 0, nil),
		buildStatCardWidget("sp-plant-throughput-max", box{3 * sitepulseStateTileCols, sitepulseKPIColumns - 3*sitepulseStateTileCols, 0, sitepulseStateBandRows},
			onPlant(SitepulseThroughputKey), "Peak throughput", SitepulseThroughputKey, "max", "t/h", 0, 0, nil),
		lowFuelCount,
		alarms,
		siteMap,
		buildEntitySelectorWidget("sp-machine-select", box{sitepulseKPIColumns, sitepulseGaugeCols, 0, 1}, "Machine", sitepulseSlotMachine),
		buildLinearGaugeWidget("sp-engine-temp", box{sitepulseKPIColumns, sitepulseGaugeCols, 1, sitepulseKPIRows - 1}, onMachine(SitepulseEngineTempKey),
			"Engine temp", SitepulseEngineTempKey, "°C", "bar", 0, sitepulseEngineTempGaugeMax, overheat),
		withThresholds(buildGaugeWidget("sp-tyre", box{sitepulseKPIColumns, sitepulseGaugeCols, sitepulseKPIRows, sitepulseMidRows}, onMachine(SitepulseTyreKey),
			"Tyre pressure",
			SitepulseTyreKey, "kPa", 0, sitepulseTyreGaugeMax),
			dashboardThreshold{Value: 0, Level: SitepulseTyreSeverity},
			dashboardThreshold{Value: SitepulseTyrePressureThreshold, Level: "normal"}),
		fleet,
	}

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
