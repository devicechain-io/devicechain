// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The expected values below are LITERALS, never read from the constants the board is
// built from: a comparison to the constant that produced a value cannot fail.

var (
	wantSitepulseDozerTokens  = []string{"sp-dozer-01", "sp-dozer-02", "sp-dozer-03", "sp-dozer-04", "sp-dozer-05", "sp-dozer-06"}
	wantSitepulseLoaderTokens = []string{"sp-loader-01", "sp-loader-02", "sp-loader-03", "sp-loader-04", "sp-loader-05", "sp-loader-06"}
	wantSitepulseHaulerTokens = []string{"sp-hauler-01", "sp-hauler-02", "sp-hauler-03", "sp-hauler-04", "sp-hauler-05", "sp-hauler-06"}
)

// wantSitepulseMachineTokens is every machine, SORTED, to compare with sortedKeys.
func wantSitepulseMachineTokens() []string {
	var all []string
	all = append(all, wantSitepulseDozerTokens...)
	all = append(all, wantSitepulseHaulerTokens...)
	all = append(all, wantSitepulseLoaderTokens...)
	sort.Strings(all)
	return all
}

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

// cardsFor returns the latest-cards selecting a measurement, keyed by the slot each binds.
func cardsFor(def dashboardDefinition, measurement string) map[string]dashboardWidget {
	out := map[string]dashboardWidget{}
	for _, w := range widgetsOfType(def, "latest-card") {
		if w.Datasource != nil && len(w.Datasource.Measurements) == 1 && w.Datasource.Measurements[0] == measurement {
			out[w.Datasource.Slot] = w
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

func TestSitepulseBoardShowsEveryMachinesFuel(t *testing.T) {
	m := sitepulseManifest(t)
	def := sitepulseBoard(t, m)
	cards := cardsFor(def, "fuel_pct")
	if got, want := sortedKeys(cards), wantSitepulseMachineTokens(); !equalStrings(got, want) {
		t.Fatalf("fuel cards bind %v, want one per machine %v", got, want)
	}
	externalIds := map[string]string{}
	for _, d := range m.Expand(m.Seed) {
		externalIds[d.Token] = d.ExternalId
	}
	if externalIds["sp-dozer-01"] != "SP-DZ-0001" {
		t.Errorf("premise: sp-dozer-01 externalId = %q, want SP-DZ-0001", externalIds["sp-dozer-01"])
	}
	for slot, w := range cards {
		if w.Options["measurement"] != "fuel_pct" || w.Options["unit"] != "%" {
			t.Errorf("%s: measurement/unit = %v/%v, want fuel_pct/%%", w.Id, w.Options["measurement"], w.Options["unit"])
		}
		if want := externalIds[slot]; want == "" || w.Options["title"] != want || def.Slots[slot].Label != want {
			t.Errorf("%s: title %v / slot label %q, want the externalId %q", w.Id, w.Options["title"], def.Slots[slot].Label, want)
		}
		if def.Slots[slot].DefaultBinding == nil || def.Slots[slot].DefaultBinding.DeviceToken != slot {
			t.Errorf("%s: slot %q does not default-bind the device it is named for", w.Id, slot)
		}
	}
}

func TestSitepulseBoardStatesTheThresholds(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	label := widgetByID(t, def, "sp-fuel-label")
	if text, _ := label.Options["text"].(string); !strings.Contains(text, "below 15 %") {
		t.Errorf("fuel label %q does not state the 15 %% low-fuel threshold", text)
	}
	if !strings.Contains(sitepulseFuelBandText(6, 6), fmt.Sprintf("%g", SitepulseLowFuelThreshold)) {
		t.Error("the fuel label is not derived from SitepulseLowFuelThreshold")
	}
	for _, c := range []struct {
		id, contains string
		min, max     float64
	}{
		{"sp-engine-temp", "above 105 °C", 0, 130},
		{"sp-tyre", "below 600 kPa", 0, 1000},
	} {
		w := widgetByID(t, def, c.id)
		if title, _ := w.Options["title"].(string); !strings.Contains(title, c.contains) {
			t.Errorf("%s title %q does not state %q", c.id, title, c.contains)
		}
		lo, _ := w.Options["min"].(float64)
		hi, _ := w.Options["max"].(float64)
		if lo != c.min || hi != c.max {
			t.Errorf("%s scale = %v..%v, want %v..%v", c.id, lo, hi, c.min, c.max)
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
	for id, c := range map[string]struct{ metric, unit string }{
		"sp-plant-throughput": {"throughput_tph", "t/h"},
		"sp-plant-running":    {"plant_running", ""},
	} {
		w := widgetByID(t, def, id)
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
	if title, _ := widgetByID(t, def, "sp-plant-running").Options["title"].(string); !strings.Contains(title, "1 = yes") {
		t.Errorf("running card title %q does not say what 1 and 0 mean", title)
	}
}

func TestSitepulseBoardPayloadIsHaulersOnly(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	cards := cardsFor(def, "payload_t")
	if got := sortedKeys(cards); !equalStrings(got, wantSitepulseHaulerTokens) {
		t.Fatalf("payload cards bind %v, want the six haul trucks %v", got, wantSitepulseHaulerTokens)
	}
	for _, w := range cards {
		if w.Options["measurement"] != "payload_t" || w.Options["unit"] != "t" {
			t.Errorf("%s: measurement/unit = %v/%v, want payload_t/t", w.Id, w.Options["measurement"], w.Options["unit"])
		}
	}
}

func TestSitepulseBoardDrillsAnAlarmToTheMachineGauges(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	sel, ok := def.Slots["selectedMachine"]
	if !ok || sel.Type != "device" || sel.DefaultBinding == nil || sel.DefaultBinding.DeviceToken != "sp-dozer-01" {
		t.Fatalf("selectedMachine = %+v, want a device slot defaulting to sp-dozer-01", sel)
	}
	if sel.Scope == nil || sel.Scope.Parent != "site" || sel.Scope.Strategy != "manual" {
		t.Errorf("selectedMachine scope = %+v, want parent site, strategy manual", sel.Scope)
	}
	if tgt := widgetByID(t, def, "sp-alarms").Options["selectionTarget"]; tgt != "selectedMachine" {
		t.Errorf("alarm table drills to %v, want selectedMachine", tgt)
	}
	if tgt := widgetByID(t, def, "sp-machine-select").Options["selectionTarget"]; tgt != "selectedMachine" {
		t.Errorf("machine selector drives %v, want selectedMachine", tgt)
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
	for _, tc := range []struct{ devices, perKind int }{{1, 1}, {3, 3}, {7, 6}, {250, 6}} {
		s, err := NewSim("sitepulse", 1, Load{DeviceCount: tc.devices})
		if err != nil {
			t.Fatalf("NewSim(%d): %v", tc.devices, err)
		}
		m := s.Manifest()
		def := sitepulseBoard(t, m)
		if got, want := len(cardsFor(def, "fuel_pct")), 3*tc.perKind; got != want {
			t.Errorf("--devices %d: %d fuel cards, want %d", tc.devices, got, want)
		}
		if got := len(cardsFor(def, "payload_t")); got != tc.perKind {
			t.Errorf("--devices %d: %d payload cards, want %d", tc.devices, got, tc.perKind)
		}
		text, _ := widgetByID(t, def, "sp-fuel-label").Options["text"].(string)
		if tc.devices > 6 {
			if want := fmt.Sprintf("first 6 of %d", tc.devices); !strings.Contains(text, want) {
				t.Errorf("--devices %d: label %q lacks the note %q", tc.devices, text, want)
			}
		} else if strings.Contains(text, "showing the first") {
			t.Errorf("--devices %d: label %q claims partial rows but every machine has a card", tc.devices, text)
		}
		if strings.Contains(text, "every machine") {
			t.Errorf("--devices %d: label %q claims every machine is listed; the anchor membership is paged", tc.devices, text)
		}
		// dashboard-management refuses a definition over 1 MiB (model/api.go maxDefinitionBytes).
		if len(m.Dashboards[0].Definition) >= 1<<20 {
			t.Errorf("--devices %d: definition is %d bytes", tc.devices, len(m.Dashboards[0].Definition))
		}
	}
}

// The default 18-machine board has exactly six per kind: every machine has a card, so
// the label must not say the rows are partial.
func TestSitepulseDefaultBoardLabelCarriesNoTruncationNote(t *testing.T) {
	text, _ := widgetByID(t, sitepulseBoard(t, sitepulseManifest(t)), "sp-fuel-label").Options["text"].(string)
	if strings.Contains(text, "showing the first") {
		t.Errorf("default board label %q claims partial rows", text)
	}
}

func TestSitepulseBoardFitsOneScreen(t *testing.T) {
	def := sitepulseBoard(t, sitepulseManifest(t))
	occupied := map[[2]int]string{}
	maxRow := 0
	for _, w := range def.Widgets {
		b := w.Layout["base"]
		if b.Col < 0 || b.Col+b.ColSpan > dashboardGridColumns {
			t.Errorf("%s spans columns %d..%d of %d", w.Id, b.Col, b.Col+b.ColSpan, dashboardGridColumns)
		}
		if b.Row+b.RowSpan > maxRow {
			maxRow = b.Row + b.RowSpan
		}
		for c := b.Col; c < b.Col+b.ColSpan; c++ {
			for r := b.Row; r < b.Row+b.RowSpan; r++ {
				if other, taken := occupied[[2]int{c, r}]; taken {
					t.Fatalf("%s overlaps %s at column %d row %d", w.Id, other, c, r)
				}
				occupied[[2]int{c, r}] = w.Id
			}
		}
	}
	if maxRow > 20 {
		t.Errorf("the board is %d rows tall, want at most 20 (about 960 px) to sit beside a 3D window", maxRow)
	}
}
