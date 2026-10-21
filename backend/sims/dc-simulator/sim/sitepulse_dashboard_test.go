// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The expected values below are LITERALS, never read from the constants the board is
// built from: a comparison to the constant that produced a value cannot fail.

// sitepulseBoard decodes the scenario's one dashboard, failing loudly when there is
// none so every test below fails by VALUE on a tree without the board.
func sitepulseBoard(t *testing.T, m SimManifest) dashboardDefinition {
	t.Helper()
	if len(m.Dashboards) != 1 {
		t.Fatalf("sitepulse declares %d dashboards, want exactly 1 (sp-dashboard)", len(m.Dashboards))
	}
	var def dashboardDefinition
	if err := json.Unmarshal([]byte(m.Dashboards[0].Definition), &def); err != nil {
		t.Fatalf("sitepulse dashboard definition does not parse: %v", err)
	}
	return def
}

func widgetsOfType(def dashboardDefinition, typ string) []dashboardWidget {
	var out []dashboardWidget
	for _, w := range def.Widgets {
		if w.Type == typ {
			out = append(out, w)
		}
	}
	return out
}

func widgetByID(t *testing.T, def dashboardDefinition, id string) dashboardWidget {
	t.Helper()
	for _, w := range def.Widgets {
		if w.Id == id {
			return w
		}
	}
	t.Fatalf("the board has no widget %q", id)
	return dashboardWidget{}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSitepulseDeclaresTheSiteDashboard(t *testing.T) {
	m := sitepulseManifest(t)
	sitepulseBoard(t, m)
	if m.Dashboards[0].Token != "sp-dashboard" {
		t.Errorf("dashboard token = %q, want sp-dashboard", m.Dashboards[0].Token)
	}
}

// asJSON renders a decoded option the way it is stored (a map marshals its keys sorted), so a
// literal can be compared as text.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The fleet is ONE table bound to the site anchor, with a column for each measurement the
// machines really report and nothing per machine: no tank, no payload card, no heading row.
func TestSitepulseBoardShowsTheFleetInOneTable(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	tables := widgetsOfType(def, "fleet-table")
	if len(tables) != 1 || tables[0].Id != "sp-fleet" {
		t.Fatalf("the board carries fleet tables %+v, want exactly sp-fleet", tables)
	}
	w := tables[0]
	if w.Datasource == nil || w.Datasource.Slot != "site" {
		t.Fatalf("sp-fleet binds %+v, want the site anchor slot", w.Datasource)
	}
	// Columns follow the datasource's measurement order. Literals: the keys the profile declares.
	if got, want := w.Datasource.Measurements, []string{"fuel_pct", "engine_temp_c", "payload_t", "tyre_pressure_kpa"}; !equalStrings(got, want) {
		t.Errorf("sp-fleet selects %v, want %v", got, want)
	}
	const columns = `[{"label":"Fuel","measurement":"fuel_pct","precision":1,"unit":"%"},` +
		`{"label":"Engine","measurement":"engine_temp_c","precision":1,"unit":"°C"},` +
		`{"label":"Payload","measurement":"payload_t","precision":1,"unit":"t"},` +
		`{"label":"Tyres","measurement":"tyre_pressure_kpa","precision":1,"unit":"kPa"}]`
	if got := asJSON(t, w.Options["columns"]); got != columns {
		t.Errorf("sp-fleet columns = %s, want %s", got, columns)
	}
	// Lowest fuel first: ascending on the fuel column.
	if w.Options["sortBy"] != "fuel_pct" {
		t.Errorf("sp-fleet sortBy = %v, want fuel_pct", w.Options["sortBy"])
	}
	if desc, _ := w.Options["sortDescending"].(bool); desc {
		t.Errorf("sp-fleet sorts descending; the lowest fuel must come first")
	}
	// One page holds the 18 machines and the plant; the table follows the board's time range.
	if n, _ := w.Options["pageSize"].(float64); n != 20 {
		t.Errorf("sp-fleet pageSize = %v, want 20", w.Options["pageSize"])
	}
	// No stale threshold: a recorded replay would dim every cell (see the board's builder).
	if _, ok := w.Options["staleAfterSeconds"]; ok {
		t.Errorf("sp-fleet sets staleAfterSeconds = %v; a replay's readings are all older than any threshold", w.Options["staleAfterSeconds"])
	}
	// One line a row: a cell shows its age only once it lags the fleet's newest reading by 30 s, a
	// LITERAL (the player reports each machine about once a second).
	if n, _ := w.Options["ageLagSeconds"].(float64); n != 30 {
		t.Errorf("sp-fleet ageLagSeconds = %v, want 30", w.Options["ageLagSeconds"])
	}
	if _, pinned := w.Options["windowSeconds"]; pinned {
		t.Errorf("sp-fleet pins its own span; it must follow the board's time range")
	}
	// Nothing per machine is left: a tank, a payload card or a heading row would repeat the table.
	for _, typ := range []string{"level-gauge", "latest-card", "label", "bar-chart"} {
		if got := widgetsOfType(def, typ); len(got) != 0 {
			t.Errorf("the board carries %d %s widgets; the fleet table replaced the per-machine cards", len(got), typ)
		}
	}
	for slot := range def.Slots {
		if strings.HasPrefix(slot, "sp-dozer") || strings.HasPrefix(slot, "sp-loader") || strings.HasPrefix(slot, "sp-hauler") {
			t.Errorf("slot %q: no machine needs a slot of its own any more", slot)
		}
	}
}

// Each column that a rule watches carries that rule's bands, and the others carry none. The
// numbers are LITERALS (the rules' 15 %, 105 C and 600 kPa and the severity each raises at); the
// gauges beside the table are held to the same ones, so the table, the gauges and the alarms agree.
func TestSitepulseFleetTableBandsAreTheRulesOwn(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	const fuel, temp, tyre = `{"level":"major","measurement":"fuel_pct","value":0},{"level":"normal","measurement":"fuel_pct","value":15}`,
		`{"level":"normal","measurement":"engine_temp_c","value":0},{"level":"critical","measurement":"engine_temp_c","value":105}`,
		`{"level":"major","measurement":"tyre_pressure_kpa","value":0},{"level":"normal","measurement":"tyre_pressure_kpa","value":600}`
	// No band for payload: nothing alarms on it.
	if got, want := asJSON(t, widgetByID(t, def, "sp-fleet").Options["thresholds"]), "["+fuel+","+temp+","+tyre+"]"; got != want {
		t.Errorf("sp-fleet thresholds = %s, want %s", got, want)
	}

	// The gauges, with the same edges (a gauge's entries carry no measurement).
	for _, c := range []struct {
		id, typ, title, thresholds string
		min, max                   float64
	}{
		// The engine gauge is a bar with ONE edge at the overheat rule's 105 C and no warning band:
		// the sim has no warning rule, so a band below the alarm would colour a reading the platform
		// does not call a problem.
		{"sp-engine-temp", "linear-gauge", "Engine temp", `[{"level":"normal","value":0},{"level":"critical","value":105}]`, 0, 130},
		{"sp-tyre", "gauge", "Tyre pressure", `[{"level":"major","value":0},{"level":"normal","value":600}]`, 0, 1000},
	} {
		w := widgetByID(t, def, c.id)
		if w.Type != c.typ {
			t.Errorf("%s is a %s, want a %s", c.id, w.Type, c.typ)
		}
		if title, _ := w.Options["title"].(string); title != c.title {
			t.Errorf("%s title = %q, want %q", c.id, title, c.title)
		}
		if got := asJSON(t, w.Options["thresholds"]); got != c.thresholds {
			t.Errorf("%s thresholds = %s, want %s", c.id, got, c.thresholds)
		}
		lo, _ := w.Options["min"].(float64)
		hi, _ := w.Options["max"].(float64)
		if lo != c.min || hi != c.max {
			t.Errorf("%s scale = %v..%v, want %v..%v", c.id, lo, hi, c.min, c.max)
		}
	}
}

// 🔴 The table has no tyre value for a dozer because the PLAYER never sends one, not because the
// board hides it: the fleet table says "does not report this" for a measurement a device never
// reported. This pins that premise on the first recorded run (a stale premise would make the
// table's honesty a claim about data it was not shown), and that nothing on the board bakes a
// per-device value or override into the table.
func TestSitepulseDozersReportNoTyrePressureInTheRecordedRun(t *testing.T) {
	raw, err := os.ReadFile("../../../../frontend/testdata/board-replay/take2-tyre-excerpt.json")
	if err != nil {
		t.Fatalf("the recorded run is not readable: %v", err)
	}
	var rec struct {
		Devices  []struct{ Token, Kind string } `json:"devices"`
		Channels struct {
			Measurements []string `json:"measurements"`
		} `json:"channels"`
		Measurements struct {
			D []int `json:"d"`
			N []int `json:"n"`
		} `json:"measurements"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	reported := map[string]map[string]bool{}
	for i, d := range rec.Measurements.D {
		kind := rec.Devices[d].Kind
		if reported[kind] == nil {
			reported[kind] = map[string]bool{}
		}
		reported[kind][rec.Channels.Measurements[rec.Measurements.N[i]]] = true
	}
	for kind, want := range map[string]map[string]bool{
		"Hauler": {"fuel_pct": true, "engine_temp_c": true, "payload_t": true, "tyre_pressure_kpa": true},
		"Loader": {"fuel_pct": true, "engine_temp_c": true, "payload_t": true, "tyre_pressure_kpa": true},
		"Dozer":  {"fuel_pct": true, "engine_temp_c": true},
		"Plant":  {"throughput_tph": true, "plant_running": true},
	} {
		if len(reported[kind]) != len(want) {
			t.Errorf("%s reports %v in the recorded run, want exactly %v", kind, reported[kind], want)
		}
		for m := range want {
			if !reported[kind][m] {
				t.Errorf("%s never reports %s in the recorded run, want it to", kind, m)
			}
		}
	}

	// And the board states nothing per device: the table is a view of what arrives.
	w := widgetByID(t, sitepulseBoard(t, sitepulseManifest(t)), "sp-fleet")
	for _, key := range []string{"rows", "values", "devices", "members", "overrides"} {
		if _, ok := w.Options[key]; ok {
			t.Errorf("sp-fleet carries a %q option; a cell must come from a reading, never from the board", key)
		}
	}
}

// The board's alarm surfaces bind the customer anchor (the whole site, plant included)
// and list ACTIVE alarms only: an operator board that lets a long-cleared row push a
// live critical off a 25-row page has the opposite job to the widget gallery.
func TestSitepulseBoardAlarmSurfacesCoverTheWholeSite(t *testing.T) {
	m := sitepulseManifest(t)
	def := sitepulseBoard(t, m)

	site, ok := def.Slots["site"]
	if !ok || site.Type != "anchor" || site.DefaultBinding == nil || site.DefaultBinding.Anchor == nil {
		t.Fatalf("the board declares no anchor slot %q: %+v", "site", site)
	}
	a := *site.DefaultBinding.Anchor
	if a.Relationship != "assigned" || a.TargetType != "customer" || a.TargetToken != "acme-earthworks" {
		t.Errorf("site anchor = %+v, want assigned -> customer/acme-earthworks", a)
	}
	// Premise pin (passes on main): the customer anchor reaches every device.
	for _, d := range m.Expand(m.Seed) {
		found := false
		for _, as := range d.Assignments {
			found = found || (as.TargetType == "customer" && as.TargetToken == "acme-earthworks")
		}
		if !found {
			t.Errorf("device %s carries no customer assignment, so the site anchor misses it", d.Token)
		}
	}

	for _, id := range []string{"sp-alarms", "sp-alarms-critical", "sp-alarms-major", "sp-map"} {
		w := widgetByID(t, def, id)
		if w.Datasource == nil || w.Datasource.Slot != "site" {
			t.Errorf("%s binds %+v, want slot site", id, w.Datasource)
		}
	}
	if st := widgetByID(t, def, "sp-alarms").Options["state"]; st != "ACTIVE" {
		t.Errorf("alarm table state filter = %v, want ACTIVE", st)
	}
	if loc := widgetByID(t, def, "sp-map").Datasource.Location; loc == nil || loc.Series != "latest" {
		t.Errorf("map location selection = %+v, want series latest", loc)
	}
}

// Every severity a machine rule raises has a count. The mapping is a LITERAL table and
// an unmapped authoring severity fails, so a new tier is a loud gap, not a skipped one.
func TestSitepulseBoardCountsEverySeverityTheRulesRaise(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	counted := map[string]bool{}
	for _, w := range widgetsOfType(def, "alarm-count") {
		if w.Options["state"] != "ACTIVE" {
			t.Errorf("%s counts state %v, want ACTIVE", w.Id, w.Options["state"])
		}
		sev, _ := w.Options["severity"].(string)
		counted[sev] = true
	}
	if !counted["CRITICAL"] || !counted["MAJOR"] || len(counted) != 2 {
		t.Errorf("counted severities = %v, want exactly CRITICAL and MAJOR", counted)
	}
	wire := map[string]string{"major": "MAJOR", "critical": "CRITICAL"}
	for _, rule := range sitepulseMachineRules() {
		sev := decodeThresholdRule(t, rule.Definition).Severity
		w, ok := wire[sev]
		if !ok {
			t.Fatalf("rule %s raises authoring severity %q, which this board has no count for", rule.Token, sev)
		}
		if !counted[w] {
			t.Errorf("rule %s raises %s but no count widget shows it", rule.Token, w)
		}
	}
}

func TestSitepulseBoardBindsThePlantThroughItsOwnSlot(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	plant, ok := def.Slots["plant"]
	if !ok || plant.Type != "device" || plant.DefaultBinding == nil || plant.DefaultBinding.DeviceToken != "sp-plant-01" {
		t.Fatalf("plant slot = %+v, want a device slot defaulting to sp-plant-01", plant)
	}
	for id, c := range map[string]struct{ typ, metric, unit string }{
		"sp-plant-throughput-now": {"stat-card", "throughput_tph", "t/h"},
		"sp-plant-throughput-avg": {"stat-card", "throughput_tph", "t/h"},
		"sp-plant-throughput-max": {"stat-card", "throughput_tph", "t/h"},
		"sp-plant-running":        {"status-light", "plant_running", ""},
	} {
		w := widgetByID(t, def, id)
		if w.Type != c.typ {
			t.Errorf("%s is a %s, want a %s", id, w.Type, c.typ)
		}
		if w.Datasource == nil || w.Datasource.Slot != "plant" ||
			len(w.Datasource.Measurements) != 1 || w.Datasource.Measurements[0] != c.metric {
			t.Errorf("%s datasource = %+v, want slot plant selecting %s", id, w.Datasource, c.metric)
		}
		if w.Options["measurement"] != c.metric {
			t.Errorf("%s measurement = %v, want %s", id, w.Options["measurement"], c.metric)
		}
		u, has := w.Options["unit"].(string)
		if u != c.unit || (c.unit == "" && has) {
			t.Errorf("%s unit = %q (present %v), want %q (omitted when empty)", id, u, has, c.unit)
		}
	}
	// "Now" is the newest reading (`last`), which the card refuses once it is older than the
	// board's range; the other two are the range's average and peak.
	for id, stat := range map[string]string{"sp-plant-throughput-now": "last", "sp-plant-throughput-avg": "avg", "sp-plant-throughput-max": "max"} {
		w := widgetByID(t, def, id)
		if w.Options["stat"] != stat {
			t.Errorf("%s stat = %v, want %s", id, w.Options["stat"], stat)
		}
		// Absent windowSeconds is what puts the card on the board's time range.
		if _, pinned := w.Options["windowSeconds"]; pinned {
			t.Errorf("%s pins its own span; it must follow the board's time range", id)
		}
		if _, banded := w.Options["thresholds"]; banded {
			t.Errorf("%s carries threshold bands, but no rule fires on throughput", id)
		}
	}
	// The trend is drawn on "now" only; the average beside it would repeat the line.
	for id, want := range map[string]bool{"sp-plant-throughput-now": true, "sp-plant-throughput-avg": false, "sp-plant-throughput-max": false} {
		if got, _ := widgetByID(t, def, id).Options["sparkline"].(bool); got != want {
			t.Errorf("%s sparkline = %v, want %v", id, got, want)
		}
	}
	// The lamp is lit by the flag itself (1 = running), and carries no band: nothing alarms on it.
	lamp := widgetByID(t, def, "sp-plant-running")
	if lamp.Options["onAt"] != 1.0 {
		t.Errorf("crusher lamp onAt = %v, want 1", lamp.Options["onAt"])
	}
	if _, banded := lamp.Options["thresholds"]; banded {
		t.Errorf("crusher lamp carries threshold bands, but no rule fires on the plant")
	}
	// The sim defines no shift target or plant capacity, so there is no honest progress-ring.
	if got := widgetsOfType(def, "progress-ring"); len(got) != 0 {
		t.Errorf("the board carries %d progress rings, but the sim defines no target or capacity to fill one against", len(got))
	}
}

func TestSitepulseBoardCarriesAnExplicitHourTimeRange(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	if def.TimeRange == nil || def.TimeRange.DefaultSeconds != 3600 {
		t.Errorf("timeRange = %+v, want an explicit 3600 s default", def.TimeRange)
	}
}

func TestSitepulseBoardDrillsAnAlarmToTheMachineGauges(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	sel, ok := def.Slots["selectedMachine"]
	// A haul truck, not the first machine: a dozer is tracked and reports no tyre pressure, so
	// a dozer default opens the board on an empty tyre gauge.
	if !ok || sel.Type != "device" || sel.DefaultBinding == nil || sel.DefaultBinding.DeviceToken != "sp-hauler-01" {
		t.Fatalf("selectedMachine = %+v, want a device slot defaulting to sp-hauler-01", sel)
	}
	if sel.Scope == nil || sel.Scope.Parent != "site" || sel.Scope.Strategy != "manual" {
		t.Errorf("selectedMachine scope = %+v, want parent site, strategy manual", sel.Scope)
	}
	if tgt := widgetByID(t, def, "sp-alarms").Options["selectionTarget"]; tgt != "selectedMachine" {
		t.Errorf("alarm table drills to %v, want selectedMachine", tgt)
	}
	// The fleet table's device name re-points the same slot, so a click on a row moves the engine
	// and tyre gauges exactly as an alarm's originator does.
	if tgt := widgetByID(t, def, "sp-fleet").Options["selectionTarget"]; tgt != "selectedMachine" {
		t.Errorf("fleet table drills to %v, want selectedMachine", tgt)
	}
	if tgt := widgetByID(t, def, "sp-machine-select").Options["selectionTarget"]; tgt != "selectedMachine" {
		t.Errorf("machine selector drives %v, want selectedMachine", tgt)
	}
	// The selector's control is labelled by its title (the widget falls back to "Select
	// selectedMachine" without one), and the frame may hide a title in a short box, so the
	// name must be a real word.
	if title := widgetByID(t, def, "sp-machine-select").Options["title"]; title != "Machine" {
		t.Errorf("machine selector title = %v, want Machine", title)
	}
	for id, metric := range map[string]string{"sp-engine-temp": "engine_temp_c", "sp-tyre": "tyre_pressure_kpa"} {
		w := widgetByID(t, def, id)
		if w.Datasource == nil || w.Datasource.Slot != "selectedMachine" ||
			len(w.Datasource.Measurements) != 1 || w.Datasource.Measurements[0] != metric {
			t.Errorf("%s datasource = %+v, want selectedMachine selecting %s", id, w.Datasource, metric)
		}
	}
}

// 🔴 NO COMMAND WIDGET YET: the console dispatch button is the next slice. Validate
// would not stop one (its far-end gate refuses a control widget only under FarEndNone;
// see TestValidateAllowsAControlBoardForAnExternalFarEnd), so this is the only thing
// holding the line. That slice deletes this test.
func TestSitepulseBoardCarriesNoCommandWidgetYet(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	if got := widgetsOfType(def, commandWidgetType); len(got) != 0 {
		t.Errorf("the board carries %d command widgets; dispatch is a later slice", len(got))
	}
}

func TestSitepulseBoardTracksTheResizedFleet(t *testing.T) {
	// The table is bound to the anchor, not to a list of machines, so a resized run changes the
	// rows it shows and nothing in the board: the same widgets, the same slots, no per-machine
	// widget, and a definition far under the 1 MiB the service refuses at.
	var first string
	for _, devices := range []int{1, 3, 7, 18, 19, 250} {
		s, err := NewSim("sitepulse", 1, Load{DeviceCount: devices})
		if err != nil {
			t.Fatalf("NewSim(%d): %v", devices, err)
		}
		m := s.Manifest()
		def := sitepulseBoard(t, m)
		ids := make([]string, 0, len(def.Widgets))
		for _, w := range def.Widgets {
			ids = append(ids, w.Id)
		}
		sort.Strings(ids)
		if got := strings.Join(ids, ","); first == "" {
			first = got
		} else if got != first {
			t.Errorf("--devices %d: widgets %s, want the same as the first run %s", devices, got, first)
		}
		if got := len(widgetsOfType(def, "fleet-table")); got != 1 {
			t.Errorf("--devices %d: %d fleet tables, want 1", devices, got)
		}
		// dashboard-management refuses a definition over 1 MiB (model/api.go maxDefinitionBytes).
		if len(m.Dashboards[0].Definition) >= 1<<20 {
			t.Errorf("--devices %d: definition is %d bytes", devices, len(m.Dashboards[0].Definition))
		}
	}
}

// Every widget ends at or above the fold of a 1080 px-tall window, so the whole board fits the
// space beside the 3D window with nothing to scroll: the alarms, the KPIs, the map, the gauges
// and the fleet table. Whether the widgets overlap or leave the grid, and whether each is a size
// it can be read at, is the frontend's to say: the parser refuses an overlap and the layout lint
// checks the rest, over the committed fixture.
func TestSitepulseBoardFitsOneScreen(t *testing.T) {
	// A LITERAL, not the constant the board is built from: 960 px across 12 columns with 8 px
	// gutters is a row pitch of ~80.7 px, and 1080 px holds 13 whole rows.
	const foldRows = 13
	if sitepulseFoldRows != foldRows {
		t.Errorf("premise: the board computes a fold of %d rows, want %d", sitepulseFoldRows, foldRows)
	}
	def := sitepulseBoard(t, sitepulseManifest(t))
	lowest := 0
	for _, w := range def.Widgets {
		b := w.Layout["base"]
		if b.Mode != "grid" {
			t.Fatalf("%s is placed %q; the fold is counted in grid rows", w.Id, b.Mode)
		}
		if end := b.Row + b.Rows; end > foldRows {
			t.Errorf("%s ends at row %d, below the fold of a 1080 px window (%d rows)", w.Id, end, foldRows)
		} else if end > lowest {
			lowest = end
		}
	}
	// And the board spends the whole screen: the fleet table takes the rows the rest leave.
	fleet := widgetByID(t, def, "sp-fleet").Layout["base"]
	if fleet.Row+fleet.Rows != foldRows || fleet.Rows != 5 || fleet.Cols != 8 {
		t.Errorf("fleet table = %+v, want eight columns over the last 5 rows (rows 8-12), beside the trends column", fleet)
	}
	// The plant's throughput is the board's headline: its band is three rows, so the figure is
	// fitted to a body that a subject line, an age footer and a caption leave room in.
	for _, id := range []string{"sp-plant-throughput-now", "sp-plant-throughput-avg", "sp-low-fuel"} {
		if b := widgetByID(t, def, id).Layout["base"]; b.Rows != 3 || b.Row != 2 || b.Cols != 3 {
			t.Errorf("%s = %+v, want a 3x3 tile at row 2", id, b)
		}
	}
	// The site's state band gives up a column per tile (two columns is the alarm count's and the
	// lamp's minimum) so the peak card keeps a place on the board beside them, two rows tall.
	for id, want := range map[string]dashboardPlacement{
		"sp-alarms-critical":      {Col: 0, Cols: 2, Row: 0, Rows: 2},
		"sp-alarms-major":         {Col: 2, Cols: 2, Row: 0, Rows: 2},
		"sp-plant-running":        {Col: 4, Cols: 2, Row: 0, Rows: 2},
		"sp-plant-throughput-max": {Col: 6, Cols: 3, Row: 0, Rows: 2},
	} {
		b := widgetByID(t, def, id).Layout["base"]
		if b.Col != want.Col || b.Cols != want.Cols || b.Row != want.Row || b.Rows != want.Rows {
			t.Errorf("%s = %+v, want %+v", id, b, want)
		}
	}
}

// The alarm table tabs by machine kind in the order the fleet is read, the map's marker click
// drives the same slot as the table's rows, and the low-fuel count is built from the fleet
// table's own fuel bands so the two cannot disagree.
func TestSitepulseBoardCarriesTheFleetAwareWidgets(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))

	alarms := widgetByID(t, def, "sp-alarms")
	if alarms.Options["groupBy"] != "deviceType" {
		t.Errorf("alarm table groupBy = %v, want deviceType", alarms.Options["groupBy"])
	}
	if got, want := asJSON(t, alarms.Options["deviceTypes"]),
		`[{"token":"sp-hauler"},{"token":"sp-loader"},{"token":"sp-dozer"},{"token":"sp-crusher-plant"}]`; got != want {
		t.Errorf("alarm table deviceTypes = %s, want %s", got, want)
	}

	if got := widgetByID(t, def, "sp-map").Options["selectionTarget"]; got != "selectedMachine" {
		t.Errorf("map selectionTarget = %v, want selectedMachine", got)
	}

	count := widgetByID(t, def, "sp-low-fuel")
	if count.Type != "fleet-count" || count.Options["show"] != "attention" || count.Options["measurement"] != SitepulseFuelKey {
		t.Errorf("low-fuel count = %s %v, want a fleet-count in attention mode over %s", count.Type, count.Options, SitepulseFuelKey)
	}
	if count.Datasource == nil || count.Datasource.Slot != "site" {
		t.Errorf("low-fuel count datasource = %+v, want the site anchor", count.Datasource)
	}
	// The same edges the fleet table colours: severity below the rule's threshold, neutral above.
	const want = `[{"level":"major","value":0},{"level":"normal","value":15}]`
	if got := asJSON(t, count.Options["thresholds"]); got != want {
		t.Errorf("low-fuel count thresholds = %s, want %s", got, want)
	}
}

// The engine temperature history of the selected machine sits beside the fleet table, with its
// alarms and commands marked. The alarm-severity breakdown is deliberately absent (see the board comment).
func TestSitepulseBoardCarriesTheEngineTrendChart(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))

	chart := widgetByID(t, def, "sp-engine-trend")
	if chart.Type != "timeseries-chart" || chart.Options["markAlarms"] != true || chart.Options["markCommands"] != true {
		t.Errorf("trend chart = %s %v, want a timeseries-chart marking alarms and commands", chart.Type, chart.Options)
	}
	if _, ok := chart.Options["windowSeconds"]; ok {
		t.Errorf("trend chart sets windowSeconds %v, want the board's range", chart.Options["windowSeconds"])
	}
	if chart.Datasource == nil || chart.Datasource.Slot != "selectedMachine" || !equalStrings(chart.Datasource.Measurements, []string{SitepulseEngineTempKey}) {
		t.Errorf("trend chart datasource = %+v, want the selected machine's engine temperature", chart.Datasource)
	}
	if b := chart.Layout["base"]; b.Row != 8 || b.Rows != 5 || b.Col != 8 || b.Cols != 4 {
		t.Errorf("trend chart = %+v, want 4x5 at column 8, row 8", b)
	}
}
