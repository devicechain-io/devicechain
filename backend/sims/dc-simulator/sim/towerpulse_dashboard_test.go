// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

type tpBoard struct {
	Slots map[string]struct {
		Type           string `json:"type"`
		DefaultBinding struct {
			Kind        string `json:"kind"`
			DeviceToken string `json:"deviceToken"`
			Anchor      struct {
				Relationship string `json:"relationship"`
				TargetType   string `json:"targetType"`
				TargetToken  string `json:"targetToken"`
			} `json:"anchor"`
		} `json:"defaultBinding"`
		Scope *struct {
			Parent   string `json:"parent"`
			Strategy string `json:"strategy"`
		} `json:"scope"`
	} `json:"slots"`
	TimeRange struct {
		DefaultSeconds int `json:"defaultSeconds"`
	} `json:"timeRange"`
	Widgets []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Datasource *struct {
			Slot         string   `json:"slot"`
			Measurements []string `json:"measurements"`
		} `json:"datasource"`
		Layout  map[string]dashboardPlacement `json:"layout"`
		Options map[string]any                `json:"options"`
	} `json:"widgets"`
}

func towerpulseBoards(t *testing.T) map[string]tpBoard {
	t.Helper()
	out := map[string]tpBoard{}
	for _, d := range towerpulseManifest(t).Dashboards {
		var b tpBoard
		if err := json.Unmarshal([]byte(d.Definition), &b); err != nil {
			t.Fatalf("%s: %v", d.Token, err)
		}
		out[d.Token] = b
	}
	return out
}

func (b tpBoard) ofType(widgetType string) []int {
	var idx []int
	for i, w := range b.Widgets {
		if w.Type == widgetType {
			idx = append(idx, i)
		}
	}
	return idx
}

func (b tpBoard) widget(t *testing.T, id string) int {
	t.Helper()
	for i, w := range b.Widgets {
		if w.ID == id {
			return i
		}
	}
	t.Fatalf("no widget %q", id)
	return -1
}

// The board carries every widget the showcase's model lists, each on the subject it is about.
func TestTowerpulseBoardCarriesTheModelsWidgets(t *testing.T) {
	b := towerpulseBoards(t)[TowerpulseDashboardToken]
	need := map[string]int{
		"entity-selector":  3, // building, floor, zone
		"alarm-severity":   1, // alarm count by severity
		"online-doughnut":  1,
		"stat-card":        4, // two building kW cards with a sparkline, two energy-per-area cards
		"device-status":    2, // the chillers
		"timeseries-chart": 2, // the zone compare chart, the chiller COP
		"alarm-table":      1,
		"command-slider":   1, // the setpoint stepper
		"history-table":    1,
		"gauge":            2,
	}
	for widgetType, want := range need {
		if got := len(b.ofType(widgetType)); got != want {
			t.Errorf("%d %s widgets, want %d", got, widgetType, want)
		}
	}
	if b.TimeRange.DefaultSeconds != 3600 {
		t.Errorf("time range default = %d, want an hour; the header's range control is what makes the window absolute", b.TimeRange.DefaultSeconds)
	}
}

// The widgets whose options are the point of the board.
func TestTowerpulseBoardWidgetsAreConfiguredForTheStory(t *testing.T) {
	b := towerpulseBoards(t)[TowerpulseDashboardToken]

	// kW cards: a sparkline, on each building's meter, on the kw metric.
	for id, slot := range map[string]string{"tp-kw-tower": towerpulseSlotMeterTower, "tp-kw-annex": towerpulseSlotMeterAnnex} {
		w := b.Widgets[b.widget(t, id)]
		if w.Options["sparkline"] != true || w.Options["measurement"] != "kw" || w.Datasource.Slot != slot {
			t.Errorf("%s = %+v", id, w)
		}
	}

	// The compare chart: one line per zone of the FLOOR, the alarms drawn on it.
	cmp := b.Widgets[b.widget(t, "tp-floor-compare")]
	if cmp.Options["groupBy"] != "device" || cmp.Options["measurement"] != "temp_c" || cmp.Options["markAlarms"] != true ||
		cmp.Datasource.Slot != towerpulseSlotFloor {
		t.Errorf("compare chart = %+v", cmp)
	}
	if _, pinned := cmp.Options["windowSeconds"]; pinned {
		t.Error("the compare chart pins its own window, so the board's range picker would not move it")
	}

	// The alarm table: active alarms, a tab per device type of the scenario, and drilling to the zone.
	at := b.Widgets[b.widget(t, "tp-alarms")]
	if at.Options["groupBy"] != "deviceType" || at.Options["state"] != AlarmStateActiveWire || at.Options["selectionTarget"] != towerpulseSlotZone {
		t.Errorf("alarm table options = %v", at.Options)
	}
	var tabs []string
	for _, tab := range at.Options["deviceTypes"].([]any) {
		tabs = append(tabs, tab.(map[string]any)["token"].(string))
	}
	var types []string
	for _, dt := range towerpulseManifest(t).DeviceTypes {
		types = append(types, dt.Token)
	}
	sort.Strings(tabs)
	sort.Strings(types)
	if strings.Join(tabs, ",") != strings.Join(types, ",") {
		t.Errorf("alarm tabs %v do not cover the scenario's device types %v", tabs, types)
	}

	// The stepper sends the profile's own command, with the profile's own schema.
	st := b.Widgets[b.widget(t, "tp-setpoint")]
	vav := profileByToken(t, towerpulseManifest(t), TowerpulseVAVProfileToken)
	var setpoint CommandSpec
	for _, c := range vav.Commands {
		if c.CommandKey == TowerpulseSetpointCommandKey {
			setpoint = c
		}
	}
	if st.Options["mode"] != "stepper" || st.Options["commandName"] != TowerpulseSetpointCommandKey ||
		st.Options["parameter"] != TowerpulseSetpointParameter || st.Datasource.Slot != towerpulseSlotZone {
		t.Errorf("setpoint control = %v", st.Options)
	}
	if st.Options["parameterSchema"] != setpoint.ParameterSchema {
		t.Errorf("the stepper bakes a schema that is not the command's: %v vs %s", st.Options["parameterSchema"], setpoint.ParameterSchema)
	}
	if !isCommandWidgetType(st.Type) {
		t.Fatal("the stepper is not a command widget")
	}

	// Status cards go stale when the chiller's own alarm would: one number, two uses.
	for _, id := range []string{"tp-chiller-tower-status", "tp-chiller-annex-status"} {
		if got := b.Widgets[b.widget(t, id)].Options["staleAfterSeconds"]; got != float64(120) {
			t.Errorf("%s stale after %v s, want the rule's 120", id, got)
		}
	}

	// The calculated metrics are shown by the cards and chart that exist for them.
	for _, id := range []string{"tp-kwh-m2-tower", "tp-kwh-m2-annex"} {
		if got := b.Widgets[b.widget(t, id)].Options["measurement"]; got != "kwh_per_m2" {
			t.Errorf("%s shows %v, want kwh_per_m2", id, got)
		}
	}
	if ds := b.Widgets[b.widget(t, "tp-annex-cop")].Datasource; ds.Slot != towerpulseSlotChillerAnnex || len(ds.Measurements) != 1 || ds.Measurements[0] != "cop" {
		t.Errorf("COP chart datasource = %+v", ds)
	}
}

// Every measurement a widget reads is one its subject's profile declares (calculated ones
// included), so no card is bound to a number nothing ever produces.
func TestTowerpulseBoardReadsOnlyMetricsItsSubjectsDeclare(t *testing.T) {
	m := towerpulseManifest(t)
	for token, b := range towerpulseBoards(t) {
		t.Run(token, func(t *testing.T) { checkTowerpulseBoardReadsOnlyDeclaredMetrics(t, m, b) })
	}
}

func checkTowerpulseBoardReadsOnlyDeclaredMetrics(t *testing.T, m SimManifest, b tpBoard) {
	t.Helper()
	profileOf := map[string]string{}
	for _, dt := range m.DeviceTypes {
		profileOf[dt.Token] = dt.ProfileToken
	}
	typeOf := map[string]string{}
	for _, d := range m.Expand(m.Seed) {
		typeOf[d.Token] = d.DeviceTypeToken
	}
	declared := func(profile string) map[string]bool {
		out := map[string]bool{}
		for _, mx := range profileByToken(t, m, profile).Metrics {
			out[mx.Key] = true
		}
		return out
	}
	checked := 0
	for _, w := range b.Widgets {
		if w.Datasource == nil {
			continue
		}
		slot := b.Slots[w.Datasource.Slot]
		// Anchor slots hold devices of every kind, so only a DEVICE slot names a profile.
		if slot.Type != "device" {
			continue
		}
		have := declared(profileOf[typeOf[slot.DefaultBinding.DeviceToken]])
		keys := append([]string(nil), w.Datasource.Measurements...)
		if mm, ok := w.Options["measurement"].(string); ok {
			keys = append(keys, mm)
		}
		if w.Type == "history-table" {
			for _, c := range w.Options["columns"].([]any) {
				keys = append(keys, c.(map[string]any)["measurement"].(string))
			}
		}
		for _, k := range keys {
			checked++
			if !have[k] {
				t.Errorf("%s reads %q from %s, whose profile does not declare it", w.ID, k, slot.DefaultBinding.DeviceToken)
			}
		}
	}
	if checked < 8 {
		t.Errorf("only %d bindings were checked; the walk has gone blind", checked)
	}
}

// Every slot a widget names is declared, every device a slot binds exists, and a scoped slot's
// parent exists.
func TestTowerpulseBoardSlotsResolve(t *testing.T) {
	m := towerpulseManifest(t)
	live := map[string]bool{}
	for _, d := range m.Expand(m.Seed) {
		live[d.Token] = true
	}
	areas := map[string]bool{}
	for _, a := range m.Areas {
		areas[a.Token] = true
	}
	customers := map[string]bool{}
	for _, c := range m.Customers {
		customers[c.Token] = true
	}
	for token, b := range towerpulseBoards(t) {
		for _, w := range b.Widgets {
			if w.Datasource != nil {
				if _, ok := b.Slots[w.Datasource.Slot]; !ok {
					t.Errorf("%s/%s binds undeclared slot %q", token, w.ID, w.Datasource.Slot)
				}
			}
			if target, ok := w.Options["selectionTarget"].(string); ok {
				if _, declared := b.Slots[target]; !declared {
					t.Errorf("%s/%s selects into undeclared slot %q", token, w.ID, target)
				}
			}
		}
		for name, s := range b.Slots {
			switch s.Type {
			case "device":
				if !live[s.DefaultBinding.DeviceToken] {
					t.Errorf("%s: slot %s binds device %q, which the scenario does not have", token, name, s.DefaultBinding.DeviceToken)
				}
			case "anchor":
				a := s.DefaultBinding.Anchor
				if !(a.TargetType == "area" && areas[a.TargetToken]) && !(a.TargetType == "customer" && customers[a.TargetToken]) {
					t.Errorf("%s: slot %s anchors on %+v, not an area or customer the scenario has", token, name, a)
				}
			}
			if s.Scope != nil {
				if _, ok := b.Slots[s.Scope.Parent]; !ok {
					t.Errorf("%s: slot %s is scoped to undeclared %q", token, name, s.Scope.Parent)
				}
			}
		}
	}
}

// The annex board is its own document, built from what a customer scoped to the annex may see:
// it opens on the annex at every level, and it is NOT the operator's board re-opened.
func TestTowerpulseAnnexBoardOpensOnTheAnnex(t *testing.T) {
	boards := towerpulseBoards(t)
	op, annex := boards[TowerpulseDashboardToken], boards[TowerpulseAnnexDashboardToken]
	if got := op.Slots[towerpulseSlotBuilding].DefaultBinding.Anchor.TargetToken; got != TowerpulseTowerToken {
		t.Errorf("operator board opens on %s", got)
	}
	if got := annex.Slots[towerpulseSlotAnnex].DefaultBinding.Anchor.TargetToken; got != TowerpulseAnnexOwnerToken {
		t.Errorf("annex board opens on %s, want the annex owner's customer", got)
	}
	if got := annex.Slots[towerpulseSlotZone].DefaultBinding.DeviceToken; !strings.HasPrefix(got, "tp-vav-annex-") {
		t.Errorf("annex board opens on zone %s", got)
	}
	if len(annex.Widgets) >= len(op.Widgets) {
		t.Errorf("the annex board has %d widgets to the operator's %d: it is the operator's board re-opened, not a customer variant", len(annex.Widgets), len(op.Widgets))
	}
}

// 🔴 TP-3. The board shared with the annex owner names nothing of the tower and offers no control:
// every device or area a slot binds is the annex's, no widget issues a command, and the building
// selector (which lists every area of the tenant) is gone.
func TestTheAnnexOwnersBoardBindsOnlyTheAnnexAndWritesNothing(t *testing.T) {
	annex := towerpulseBoards(t)[TowerpulseAnnexDashboardToken]
	for name, s := range annex.Slots {
		switch s.Type {
		case "device":
			tok := s.DefaultBinding.DeviceToken
			if !strings.Contains(tok, "annex") || strings.Contains(tok, "tower") {
				t.Errorf("slot %s binds %s, which is not the annex's", name, tok)
			}
		case "anchor":
			tok := s.DefaultBinding.Anchor.TargetToken
			if tok != TowerpulseAnnexOwnerToken {
				t.Errorf("slot %s anchors on %s, which is not the annex's", name, tok)
			}
		}
	}
	for _, w := range annex.Widgets {
		switch w.Type {
		case commandSliderWidgetType, commandToggleWidgetType, "command-button":
			t.Errorf("%s is a %s: the annex owner is read-only and a control is a write affordance", w.ID, w.Type)
		}
		if _, ok := w.Options["commandName"]; ok {
			t.Errorf("%s carries a command", w.ID)
		}
		if w.ID == "tp-building-select" {
			t.Errorf("the building selector lists every area of the tenant, the tower included")
		}
		if strings.Contains(w.ID, "tower") {
			t.Errorf("%s is a tower widget on the annex board", w.ID)
		}
	}
	raw, _ := json.Marshal(annex)
	for _, banned := range []string{"tp-tower", "-tower-", "tp-chiller-tower", "tp-meter-tower", "set-setpoint", "setpoint"} {
		if strings.Contains(strings.ToLower(string(raw)), banned) {
			t.Errorf("the annex board's definition mentions %q", banned)
		}
	}
	// And the operator's board keeps what the annex board gave up: the cross-campus view and the control.
	op := towerpulseBoards(t)[TowerpulseDashboardToken]
	if len(op.ofType("command-slider")) != 1 || op.Slots[towerpulseSlotChillerTower].DefaultBinding.DeviceToken != "tp-chiller-tower" {
		t.Error("the operator's board lost its tower chiller or its setpoint control")
	}
}

// Widgets sit on the 12-column grid without overlapping, and the board's controls are answered:
// the scenario declares an in-process far end, so a command widget is legal on it (Validate refuses
// one against a scenario nobody answers for).
func TestTowerpulseBoardLayoutIsAGridWithoutOverlap(t *testing.T) {
	for token, b := range towerpulseBoards(t) {
		type rect struct{ c0, c1, r0, r1 int }
		var rects []rect
		for _, w := range b.Widgets {
			p := w.Layout["base"]
			if p.Col < 0 || p.Col+p.Cols > dashboardGridColumns || p.Cols < 1 || p.Rows < 1 {
				t.Errorf("%s/%s is off the grid: %+v", token, w.ID, p)
			}
			r := rect{p.Col, p.Col + p.Cols, p.Row, p.Row + p.Rows}
			for i, o := range rects {
				if r.c0 < o.c1 && o.c0 < r.c1 && r.r0 < o.r1 && o.r0 < r.r1 {
					t.Errorf("%s/%s overlaps %s", token, w.ID, b.Widgets[i].ID)
				}
			}
			rects = append(rects, r)
		}
	}
	m := towerpulseManifest(t)
	m.CommandFarEnd = FarEndNone
	if err := m.Validate(); err == nil {
		t.Error("a board with a command widget validated against a scenario nothing answers for")
	}
}

// 🔴 TP-3 (second finding). A member limited to a customer is refused every area anchor and the
// relationship graph (FORBIDDEN, SCOPE_UNSUPPORTED), so a board anchored on areas opens for them with
// its context dropped, empty pickers and disabled aggregates. The annex board therefore carries no
// area anchor, roots on the annex owner's customer under `assigned` (what customerDevices answers
// for that member), and every device it binds is one of that customer's own devices.
func TestTheAnnexBoardBindsOnlyWhatACustomerScopedCallerCanResolve(t *testing.T) {
	m := towerpulseManifest(t)
	owner := m.CustomerUsers[0].CustomerToken
	scoped := map[string]bool{}
	for _, d := range m.Expand(m.Seed) {
		for _, a := range d.Assignments {
			if a.TargetType == "customer" && a.TargetToken == owner {
				scoped[d.Token] = true
			}
		}
	}
	if len(scoped) == 0 {
		t.Fatal("no device is assigned to the annex owner, so every check below would pass vacuously")
	}
	annex := towerpulseBoards(t)[TowerpulseAnnexDashboardToken]
	roots := 0
	for name, s := range annex.Slots {
		switch s.Type {
		case "anchor":
			roots++
			a := s.DefaultBinding.Anchor
			if a.TargetType != "customer" || a.TargetToken != owner || a.Relationship != assignmentRelationshipType {
				t.Errorf("slot %s anchors on %s %s (%s): only the member's own customer under %q resolves for them",
					name, a.TargetType, a.TargetToken, a.Relationship, assignmentRelationshipType)
			}
		case "device":
			if tok := s.DefaultBinding.DeviceToken; !scoped[tok] {
				t.Errorf("slot %s binds %s, which is not one of the annex owner's devices", name, tok)
			}
			if s.Scope != nil && annex.Slots[s.Scope.Parent].Type != "anchor" {
				t.Errorf("slot %s is scoped under %s, which is not an anchor", name, s.Scope.Parent)
			}
		}
	}
	if roots != 1 {
		t.Errorf("%d anchor slots, want the one customer anchor", roots)
	}
	for _, w := range annex.Widgets {
		if w.Datasource != nil {
			if _, ok := annex.Slots[w.Datasource.Slot]; !ok {
				t.Errorf("%s reads slot %q, which the board does not declare", w.ID, w.Datasource.Slot)
			}
		}
		if w.Type == "entity-selector" {
			target, _ := w.Options["selectionTarget"].(string)
			if s, ok := annex.Slots[target]; !ok || s.Scope == nil {
				t.Errorf("%s picks slot %q, which is not a scoped device slot under the customer anchor", w.ID, target)
			}
		}
	}
	raw := strings.ToLower(string(mustJSON(t, annex)))
	for _, banned := range []string{`"targettype":"area"`, `"floor"`, `"building"`} {
		if strings.Contains(raw, banned) {
			t.Errorf("the annex board's definition still carries %s", banned)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
