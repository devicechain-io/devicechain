// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// ---- The manifest and the provisioning shape ---------------------------------------------------

func towerpulseManifest(t *testing.T) SimManifest {
	t.Helper()
	m := NewTowerpulse(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("the towerpulse manifest does not validate: %v", err)
	}
	return m
}

func devicesByType(m SimManifest) map[string][]DeviceInstance {
	out := map[string][]DeviceInstance{}
	for _, d := range m.Expand(m.Seed) {
		out[d.DeviceTypeToken] = append(out[d.DeviceTypeToken], d)
	}
	return out
}

func metricKeys(p ProfileSpec) []string {
	out := make([]string, len(p.Metrics))
	for i, m := range p.Metrics {
		out[i] = m.Key
	}
	return out
}

// The campus: 31 devices of four kinds, split across a tower and an annex, exactly as the
// showcase's model states it.
func TestTowerpulseDeclaresTheCampus(t *testing.T) {
	m := towerpulseManifest(t)
	by := devicesByType(m)
	for deviceType, want := range map[string]int{
		TowerpulseVAVTypeToken: 24, TowerpulseAHUTypeToken: 3, TowerpulseChillerTypeToken: 2, TowerpulseMeterTypeToken: 2,
	} {
		if got := len(by[deviceType]); got != want {
			t.Errorf("%s: %d devices, want %d", deviceType, got, want)
		}
	}
	if got := DeviceCount(m); got != 31 {
		t.Errorf("DeviceCount = %d, want 31", got)
	}
	tower, annex := 0, 0
	for _, d := range by[TowerpulseVAVTypeToken] {
		switch {
		case strings.HasPrefix(d.Token, "tp-vav-tower-"):
			tower++
		case strings.HasPrefix(d.Token, "tp-vav-annex-"):
			annex++
		}
	}
	if tower != 20 || annex != 4 {
		t.Errorf("zones: %d in the tower and %d in the annex, want 20 and 4", tower, annex)
	}
	if !m.FixedTopology {
		t.Error("the scenario names devices chosen for their behaviour; it must refuse a device-count override")
	}
	if _, err := NewSim("towerpulse", 1, Load{DeviceCount: 5}); err == nil {
		t.Error("NewSim accepted --devices for a fixed topology")
	}
	if m.FarEndMode() != FarEndInternal {
		t.Errorf("far end is %q; this process answers the campus's commands", m.FarEndMode())
	}
}

// The metric and command vocabulary is exactly the one the showcase's model lists.
func TestTowerpulseProfilesCarryTheModelsMetricsAndCommands(t *testing.T) {
	m := towerpulseManifest(t)
	cases := []struct {
		profile  string
		metrics  []string
		commands []string
	}{
		{TowerpulseVAVProfileToken, []string{"temp_c", "setpoint_c", "co2_ppm", "occupancy", "damper_pct"}, []string{"boost-cooling", "set-setpoint"}},
		{TowerpulseAHUProfileToken, []string{"supply_air_c", "fan_pct", "filter_dp_pa", "mode"}, []string{"set-mode"}},
		{TowerpulseChillerProfileToken, []string{"chw_supply_c", "power_kw", "cooling_kw", "running", "cop"}, []string{"restart"}},
		{TowerpulseMeterProfileToken, []string{"kw", "kwh", "kwh_per_m2"}, nil},
	}
	for _, c := range cases {
		p := profileByToken(t, m, c.profile)
		if got := metricKeys(p); strings.Join(got, ",") != strings.Join(c.metrics, ",") {
			t.Errorf("%s metrics = %v, want %v", c.profile, got, c.metrics)
		}
		var keys []string
		for _, cmd := range p.Commands {
			keys = append(keys, cmd.CommandKey)
			var schema []map[string]any
			if cmd.ParameterSchema != "" {
				if err := json.Unmarshal([]byte(cmd.ParameterSchema), &schema); err != nil {
					t.Errorf("%s: parameter schema is not a JSON array: %v", cmd.Token, err)
				}
			}
		}
		if strings.Join(keys, ",") != strings.Join(c.commands, ",") {
			t.Errorf("%s commands = %v, want %v", c.profile, keys, c.commands)
		}
	}

	// The typed definitions say what each command takes. boost-cooling is minutes, and the
	// setpoint is bounded so the console's stepper has a range to render.
	vav := profileByToken(t, m, TowerpulseVAVProfileToken)
	for _, cmd := range vav.Commands {
		var params []map[string]any
		_ = json.Unmarshal([]byte(cmd.ParameterSchema), &params)
		if len(params) != 1 {
			t.Fatalf("%s declares %d parameters, want 1", cmd.CommandKey, len(params))
		}
		switch cmd.CommandKey {
		case "boost-cooling":
			if params[0]["name"] != "minutes" || params[0]["dataType"] != "INT" {
				t.Errorf("boost-cooling parameter = %v, want INT minutes", params[0])
			}
		case "set-setpoint":
			if params[0]["name"] != TowerpulseSetpointParameter || params[0]["minValue"] == nil || params[0]["maxValue"] == nil {
				t.Errorf("set-setpoint parameter = %v, want a bounded %s", params[0], TowerpulseSetpointParameter)
			}
		}
	}
	// set-mode is an enum, so the console renders a select.
	ahu := profileByToken(t, m, TowerpulseAHUProfileToken)
	if !strings.Contains(ahu.Commands[0].ParameterSchema, `"enum":["normal","economy","boost"]`) {
		t.Errorf("set-mode schema = %s", ahu.Commands[0].ParameterSchema)
	}
}

// Everything is assigned: a device carries its floor (or plant), its building and its owner, so
// every event it sends is stamped with a building and a customer anchor.
func TestTowerpulseAssignsEveryDeviceToAFloorABuildingAndAnOwner(t *testing.T) {
	m := towerpulseManifest(t)
	areaType := map[string]string{}
	for _, a := range m.Areas {
		areaType[a.Token] = a.AreaTypeToken
	}
	for _, d := range m.Expand(m.Seed) {
		var buildings, floors, customers []string
		for _, a := range d.Assignments {
			switch a.TargetType {
			case "area":
				switch areaType[a.TargetToken] {
				case TowerpulseBuildingTypeToken:
					buildings = append(buildings, a.TargetToken)
				case TowerpulseFloorTypeToken, TowerpulsePlantTypeToken:
					floors = append(floors, a.TargetToken)
				default:
					t.Errorf("%s is assigned to area %q of unknown type", d.Token, a.TargetToken)
				}
			case "customer":
				customers = append(customers, a.TargetToken)
			}
		}
		if len(buildings) != 1 || len(floors) != 1 || len(customers) != 1 {
			t.Errorf("%s: buildings %v floors %v customers %v, want one of each", d.Token, buildings, floors, customers)
			continue
		}
		wantOwner := TowerpulseTowerOwnerToken
		if buildings[0] == TowerpulseAnnexToken {
			wantOwner = TowerpulseAnnexOwnerToken
		}
		if customers[0] != wantOwner {
			t.Errorf("%s is in %s but belongs to %s, want %s", d.Token, buildings[0], customers[0], wantOwner)
		}
		// The floor belongs to the same building as the device: "tp-tower-f4" under "tp-tower".
		if !strings.HasPrefix(floors[0], buildings[0]+"-") {
			t.Errorf("%s: floor %s is not in building %s", d.Token, floors[0], buildings[0])
		}
	}
	// One area per floor and plant, and the two buildings.
	if got, want := len(m.Areas), 2+(5+1)+(2+1); got != want {
		t.Errorf("%d areas, want %d", got, want)
	}
}

// The energy meters carry their building's floor area as a device constant, and nothing else does.
func TestTowerpulseMetersCarryTheirFloorArea(t *testing.T) {
	m := towerpulseManifest(t)
	for _, d := range m.Expand(m.Seed) {
		if d.DeviceTypeToken != TowerpulseMeterTypeToken {
			if d.Metadata != "" {
				t.Errorf("%s carries metadata %s but only a meter reads a constant", d.Token, d.Metadata)
			}
			continue
		}
		var meta map[string]float64
		if err := json.Unmarshal([]byte(d.Metadata), &meta); err != nil {
			t.Fatalf("%s: metadata %q: %v", d.Token, d.Metadata, err)
		}
		want := map[string]float64{"tp-meter-tower": 6200, "tp-meter-annex": 1850}[d.Token]
		if len(meta) != 1 || meta[TowerpulseFloorAreaConstant] != want || want == 0 {
			t.Errorf("%s metadata = %v, want {%s: %v}", d.Token, meta, TowerpulseFloorAreaConstant, want)
		}
	}
}

// Two metrics are calculated, each on its own profile, and the formulas COMPILE under the
// platform's own compiler against the profile's measured metrics and the device constant. The
// ceiling is per profile, so one per profile is what the tier must allow.
func TestTowerpulseCalculatedMetricsCompileWithThePlatformCompiler(t *testing.T) {
	m := towerpulseManifest(t)
	calculated := map[string]string{}
	for _, p := range m.Profiles {
		n := 0
		var measured []calcfield.Input
		var defs []calcfield.Definition
		for _, mx := range p.Metrics {
			ty, ok := calcfield.ParseType(mx.DataType)
			if !ok {
				t.Fatalf("%s/%s: data type %q is not one a formula can read", p.Token, mx.Key, mx.DataType)
			}
			if mx.Expression == "" {
				measured = append(measured, calcfield.Input{Key: mx.Key, Type: ty})
				continue
			}
			n++
			calculated[mx.Key] = mx.Expression
			defs = append(defs, calcfield.Definition{Key: mx.Key, Expression: mx.Expression, Type: ty})
		}
		if n > towerpulseCalculatedMetricsPerProfile {
			t.Errorf("%s declares %d calculated metrics; the scenario's stated ceiling is %d per profile",
				p.Token, n, towerpulseCalculatedMetricsPerProfile)
		}
		if len(defs) == 0 {
			continue
		}
		plan, err := calcfield.NewPlan(measured, defs)
		if err != nil {
			t.Fatalf("%s: %v", p.Token, err)
		}
		if problems := plan.Problems(); len(problems) > 0 {
			t.Fatalf("%s: the platform's compiler refuses a formula: %v", p.Token, problems)
		}
		// And it computes the right thing for a reading and a constant.
		for i := 0; i < plan.Len(); i++ {
			s := plan.Env().Sample()
			switch plan.Step(i).Key {
			case TowerpulseCOPKey:
				mustSet(t, s, TowerpulseCoolingKey, "300")
				mustSet(t, s, TowerpulsePowerKey, "60")
				mustSet(t, s, TowerpulseChillerSupplyKey, "6.5")
				mustSet(t, s, TowerpulseRunningKey, "1")
				if got := plan.Eval(s, i); !got.OK() || got.Value != "5" {
					t.Errorf("cop of 300 kW cooling on 60 kW = %+v, want 5", got)
				}
			case TowerpulseKwhPerM2Key:
				mustSet(t, s, TowerpulseMeterKwKey, "100")
				mustSet(t, s, TowerpulseMeterKwhKey, "12400")
				s.UseConstants([]calcfield.Constant{calcfield.NewConstant(TowerpulseFloorAreaConstant, 6200)})
				if got := plan.Eval(s, i); !got.OK() || got.Value != "2" {
					t.Errorf("12400 kWh over 6200 m2 = %+v, want 2", got)
				}
			}
		}
	}
	if len(calculated) != 2 || calculated[TowerpulseCOPKey] != "cooling_kw / power_kw" ||
		calculated[TowerpulseKwhPerM2Key] != "kwh / device.floor_area_m2" {
		t.Errorf("calculated metrics = %v, want cop = cooling_kw / power_kw and kwh_per_m2 = kwh / device.floor_area_m2", calculated)
	}
}

func mustSet(t *testing.T, s *calcfield.Sample, key, raw string) {
	t.Helper()
	if _, err := s.Set(key, raw); err != nil {
		t.Fatalf("set %s=%s: %v", key, raw, err)
	}
}

// A calculated metric is computed by the platform, so the device must NEVER report it: a value a
// device sends under a calculated key is dropped, which would read as the model being broken.
func TestTowerpulseDevicesNeverReportACalculatedMetric(t *testing.T) {
	m := towerpulseManifest(t)
	calculated := map[string]bool{}
	for _, p := range m.Profiles {
		for _, mx := range p.Metrics {
			if mx.Expression != "" {
				calculated[mx.Key] = true
			}
		}
	}
	series, _ := tpRun(t, nil, 600, 60)
	for tok, samples := range series {
		for _, s := range samples {
			for key := range s.m {
				if calculated[key] {
					t.Fatalf("%s reports calculated metric %q", tok, key)
				}
			}
		}
	}
	// Every measured metric of every profile IS reported (a declared metric nothing sends is a
	// card that stays empty).
	reported := map[string]map[string]bool{}
	for tok, samples := range series {
		reported[tok] = map[string]bool{}
		for k := range samples[0].m {
			reported[tok][k] = true
		}
	}
	profileOf := map[string]string{}
	for _, dt := range m.DeviceTypes {
		profileOf[dt.Token] = dt.ProfileToken
	}
	for _, d := range m.Expand(m.Seed) {
		for _, mx := range profileByToken(t, m, profileOf[d.DeviceTypeToken]).Metrics {
			if mx.Expression == "" && !reported[d.Token][mx.Key] {
				t.Errorf("%s never reports declared metric %q", d.Token, mx.Key)
			}
		}
	}
}

// ---- The rules --------------------------------------------------------------------------------

// R1..R4 are the rules the showcase's model lists, authored as the platform expects them and
// wired to a command the profile really declares.
func TestTowerpulseRulesAreTheModelsFour(t *testing.T) {
	rules := tpRules(t)
	if len(rules) != 4 {
		t.Fatalf("%d rules, want 4", len(rules))
	}
	for token, r := range rules {
		if !r.Spec.Enabled {
			t.Errorf("%s is disabled: publish-time validation only gates ENABLED rules, so it would be published unchecked and never fire", token)
		}
	}

	r1 := decodeTPRule(t, rules[TowerpulseZoneHotRuleToken].Spec)
	if r1.Type != "aggregate" || r1.WindowMode != "sliding" || r1.Agg != "avg" || r1.Metric != TowerpulseTempKey {
		t.Errorf("R1 is %+v, want a sliding average of %s", r1, TowerpulseTempKey)
	}
	if r1.Op != "gt" || r1.Threshold != 26 || r1.Window != "5m0s" || r1.Severity != "major" {
		t.Errorf("R1 = op %q threshold %v window %q severity %q, want gt 26 over 5m0s at major", r1.Op, r1.Threshold, r1.Window, r1.Severity)
	}
	if r1.When.Metric != TowerpulseOccupancyKey || r1.When.Op != "gt" || r1.When.Threshold == nil || *r1.When.Threshold != 0 {
		t.Errorf("R1 gate = %+v, want occupancy > 0", r1.When)
	}
	if len(r1.Actions) != 2 || r1.Actions[0].Type != "raiseAlarm" || r1.Actions[1].Type != "sendCommand" {
		t.Fatalf("R1 actions = %+v, want raiseAlarm then sendCommand", r1.Actions)
	}
	send := r1.Actions[1].SendCommand
	if send.Command != "boost-cooling" || send.Payload != `{"minutes":15}` {
		t.Errorf("R1 sends %q with payload %q, want boost-cooling {\"minutes\":15}", send.Command, send.Payload)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(send.Payload), &payload); err != nil || payload["minutes"] != float64(TowerpulseBoostMinutes) {
		t.Errorf("payload %q is not the minutes object (%v)", send.Payload, err)
	}

	r2 := decodeTPRule(t, rules[TowerpulseCO2RuleToken].Spec)
	if r2.Type != "threshold" || r2.When.Metric != TowerpulseCO2Key || r2.When.Op != "gt" || *r2.When.Threshold != 1000 || r2.Severity != "minor" || len(r2.Actions) != 1 {
		t.Errorf("R2 = %+v", r2)
	}
	r3 := decodeTPRule(t, rules[TowerpulseFilterRuleToken].Spec)
	if r3.Type != "threshold" || r3.When.Metric != TowerpulseFilterDPKey || r3.When.Op != "gt" || *r3.When.Threshold != 250 || r3.Severity != "minor" {
		t.Errorf("R3 = %+v", r3)
	}
	// R3 is the human beat: it alarms and sends NOTHING.
	if len(r3.Actions) != 1 || r3.Actions[0].Type != "raiseAlarm" {
		t.Errorf("R3 actions = %+v, want a lone raiseAlarm", r3.Actions)
	}
	r4 := decodeTPRule(t, rules[TowerpulseChillerRuleToken].Spec)
	if r4.Type != "absence" || r4.Timeout != "2m0s" || r4.Severity != "critical" || len(r4.Actions) != 1 {
		t.Errorf("R4 = %+v", r4)
	}

	// Each rule is on the profile whose devices it is about.
	for token, want := range map[string]string{
		TowerpulseZoneHotRuleToken: TowerpulseVAVProfileToken, TowerpulseCO2RuleToken: TowerpulseVAVProfileToken,
		TowerpulseFilterRuleToken: TowerpulseAHUProfileToken, TowerpulseChillerRuleToken: TowerpulseChillerProfileToken,
	} {
		if rules[token].Profile != want {
			t.Errorf("%s is on %s, want %s", token, rules[token].Profile, want)
		}
	}
}

// Validate refuses a rule whose command the profile does not declare: the seam that otherwise
// dead-letters at the first firing. R1's command is checked here against the vocabulary, and a
// typo is refused.
func TestTowerpulseRuleCommandIsCheckedAgainstTheProfile(t *testing.T) {
	m := towerpulseManifest(t)
	for i, p := range m.Profiles {
		if p.Token != TowerpulseVAVProfileToken {
			continue
		}
		rules := append([]DetectionRuleSpec(nil), p.DetectionRules...)
		bad := AggregateAlarmRule{
			Token: "tp-rule-typo", Name: "typo", Metric: TowerpulseTempKey, Op: OpGt, Threshold: 26,
			Window: TowerpulseZoneHotWindow, Severity: SeverityMajor, AlarmKey: "typo",
			CommandKey: "boost-coolng", Enabled: true,
		}.Spec()
		m.Profiles[i].DetectionRules = append(rules, bad)
	}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "boost-coolng") {
		t.Errorf("Validate = %v, want a refusal naming the undeclared command", err)
	}
}

// The gate's metric is checked against the profile too: a gate on a metric the profile does not
// report passes nothing, so the rule would be published and never fire.
func TestTowerpulseRuleGateMetricMustBeDeclared(t *testing.T) {
	m := towerpulseManifest(t)
	for i, p := range m.Profiles {
		if p.Token != TowerpulseVAVProfileToken {
			continue
		}
		bad := AggregateAlarmRule{
			Token: "tp-rule-gate", Name: "gate", Metric: TowerpulseTempKey, Op: OpGt, Threshold: 26,
			Window: TowerpulseZoneHotWindow, GateMetric: "headcount", GateOp: OpGt, Severity: SeverityMajor, AlarmKey: "g", Enabled: true,
		}.Spec()
		m.Profiles[i].DetectionRules = append(append([]DetectionRuleSpec(nil), p.DetectionRules...), bad)
	}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "headcount") {
		t.Errorf("Validate = %v, want a refusal naming the undeclared gate metric", err)
	}
}

// ---- The rules FIRE on the model's telemetry, and nothing else does ---------------------------

const tpRunSeconds = 2400.0

func tpFireAll(t *testing.T, series map[string][]tpSample, rules map[string]struct {
	Spec    DetectionRuleSpec
	Profile string
}) map[string][]string {
	t.Helper()
	typeOf := map[string]string{
		TowerpulseVAVProfileToken: TowerpulseVAVTypeToken, TowerpulseAHUProfileToken: TowerpulseAHUTypeToken,
		TowerpulseChillerProfileToken: TowerpulseChillerTypeToken, TowerpulseMeterProfileToken: TowerpulseMeterTypeToken,
	}
	fired := map[string][]string{}
	for token, r := range rules {
		for _, dev := range tpDevicesOfType(t, typeOf[r.Profile]) {
			if tpRaisedAt(tpEval(t, r.Spec, series[dev], tpRunSeconds)) >= 0 {
				fired[token] = append(fired[token], dev)
			}
		}
		sort.Strings(fired[token])
	}
	return fired
}

// Each rule fires on the device the script names, and on no other. An alarm on the board is one
// the script raised; the model never drifts into one.
func TestTowerpulseScriptCrossesEachRuleAndNothingElseDoes(t *testing.T) {
	series, _ := tpRun(t, nil, tpRunSeconds, 5)
	fired := tpFireAll(t, series, tpRules(t))
	want := map[string][]string{
		TowerpulseZoneHotRuleToken: {TowerpulseHotZone},
		TowerpulseCO2RuleToken:     {TowerpulseStuffyZone},
		TowerpulseFilterRuleToken:  {TowerpulseClogAHU},
		TowerpulseChillerRuleToken: {TowerpulseDropChiller},
	}
	for token, devices := range want {
		if strings.Join(fired[token], ",") != strings.Join(devices, ",") {
			t.Errorf("%s fired on %v, want exactly %v", token, fired[token], devices)
		}
	}
	for token := range fired {
		if _, ok := want[token]; !ok {
			t.Errorf("unexpected rule %s fired", token)
		}
	}
}

// R1 raises when the five-minute average passes 26 C, which is a few minutes after the meeting
// starts and well after the room first warms: it is a sustained condition, not a spike. The
// numbers are read off the telemetry and the document, not off the constants.
func TestTowerpulseZoneHotRaisesOnASustainedAverageNotASpike(t *testing.T) {
	series, _ := tpRun(t, nil, tpRunSeconds, 5)
	spec := tpRules(t)[TowerpulseZoneHotRuleToken].Spec
	f := tpEval(t, spec, series[TowerpulseHotZone], tpRunSeconds)
	raised := tpRaisedAt(f)
	if raised < 300 || raised > 720 {
		t.Fatalf("R1 raised at %v s, want between 5 and 12 minutes in (an average over 5 minutes cannot raise sooner)", raised)
	}
	// The instantaneous temperature passed 26 C LONG before the average did: that is the
	// difference between this rule and a threshold rule.
	firstAbove := -1.0
	for _, s := range series[TowerpulseHotZone] {
		if s.m[TowerpulseTempKey] > 26 {
			firstAbove = s.t
			break
		}
	}
	if firstAbove < 0 || firstAbove >= raised-60 {
		t.Errorf("the reading passed 26 C at %v s and the rule raised at %v s: the average should lag by about a minute", firstAbove, raised)
	}
}

// The same heat in a room nobody is in raises nothing: R1 is gated on occupancy. The empty sunny
// zone really does average above 26 C for minutes (so the gate is what silences it, not a cool
// room), and with the gate removed from the document it WOULD fire.
func TestTowerpulseOccupancyGateSilencesAnEmptyHotRoom(t *testing.T) {
	series, _ := tpRun(t, nil, tpRunSeconds, 5)
	spec := tpRules(t)[TowerpulseZoneHotRuleToken].Spec

	empty := series[TowerpulseSunnyEmptyZone]
	hot := 0
	for _, s := range empty {
		if s.m[TowerpulseTempKey] > 28 {
			hot++
		}
		if s.m[TowerpulseOccupancyKey] != 0 {
			t.Fatalf("the sunny zone has %v people in it; it is the empty control", s.m[TowerpulseOccupancyKey])
		}
	}
	if hot < 20 {
		t.Fatalf("the empty zone is above 28 C for only %d samples, so it is not a test of the gate", hot)
	}
	if got := tpEval(t, spec, empty, tpRunSeconds); len(got) != 0 {
		t.Errorf("R1 fired on an unoccupied room: %+v", got)
	}

	// Negative control: the same rule WITHOUT its gate fires on the empty room. If this stopped
	// failing the gate, the assertion above would be passing for another reason.
	ungated := spec
	var doc map[string]any
	_ = json.Unmarshal([]byte(spec.Definition), &doc)
	delete(doc, "when")
	raw, _ := json.Marshal(doc)
	ungated.Definition = string(raw)
	if tpRaisedAt(tpEval(t, ungated, empty, tpRunSeconds)) < 0 {
		t.Error("with the gate removed R1 still did not fire on the hot empty room, so the room is not what the control assumes")
	}
}

// NEGATIVE CONTROLS, which are what make the firing tests above mean something.
//
//   - Script off: the same campus, same rules, same duration, and NOTHING fires. An alarm in the
//     test above is the script's doing and not the model drifting, or a rule firing on noise.
//   - A disabled rule fires nothing, even on the script's own heat.
//   - R4 with the drop disabled never fires: the chiller is merely quiet in the sense of a gap
//     between ticks.
func TestTowerpulseRuleFiringHasNegativeControls(t *testing.T) {
	rules := tpRules(t)

	quiet, _ := tpRun(t, func(m *towerpulseModel) { m.script = false; m.dropAt = -1 }, tpRunSeconds, 5)
	if fired := tpFireAll(t, quiet, rules); len(fired) != 0 {
		t.Errorf("with the script off these rules fired anyway: %v", fired)
	}

	series, _ := tpRun(t, nil, tpRunSeconds, 5)
	disabled := towerpulseZoneHotRule(false)
	if got := tpEval(t, disabled, series[TowerpulseHotZone], tpRunSeconds); len(got) != 0 {
		t.Errorf("a disabled R1 fired: %+v", got)
	}
	if tpRaisedAt(tpEval(t, towerpulseZoneHotRule(true), series[TowerpulseHotZone], tpRunSeconds)) < 0 {
		t.Error("the enabled twin of the disabled rule did not fire, so the control above proves nothing")
	}

	noDrop, _ := tpRun(t, func(m *towerpulseModel) { m.dropAt = -1 }, tpRunSeconds, 5)
	r4 := rules[TowerpulseChillerRuleToken].Spec
	if got := tpEval(t, r4, noDrop[TowerpulseDropChiller], tpRunSeconds); len(got) != 0 {
		t.Errorf("R4 fired on a chiller that never dropped: %+v", got)
	}
}

// R4 raises two minutes after the last reading the chiller sent, on the annex chiller only, and
// the tower chiller beside it keeps reporting.
func TestTowerpulseChillerSilenceRaisesTwoMinutesAfterTheLastReading(t *testing.T) {
	series, _ := tpRun(t, nil, tpRunSeconds, 5)
	annex := series[TowerpulseDropChiller]
	last := annex[len(annex)-1].t
	if last > towerpulseChillerDropSecond || last < towerpulseChillerDropSecond-5 {
		t.Fatalf("the annex chiller's last reading is at %v s, want within one tick of the drop at %v s", last, towerpulseChillerDropSecond)
	}
	f := tpEval(t, tpRules(t)[TowerpulseChillerRuleToken].Spec, annex, tpRunSeconds)
	if tpRaisedAt(f) != last+120 {
		t.Errorf("R4 raised at %v s, want %v s (two minutes after the last reading)", tpRaisedAt(f), last+120)
	}
	tower := series["tp-chiller-tower"]
	if tower[len(tower)-1].t != tpRunSeconds {
		t.Errorf("the tower chiller stopped reporting at %v s", tower[len(tower)-1].t)
	}
}

// A presenter can drop a chiller on cue; Tick and the model agree that a dropped chiller says
// nothing (an empty reading is how EmitAll knows to send nothing).
func TestTowerpulseDroppedChillerIsSilentAndOthersAreNot(t *testing.T) {
	model := newTowerpulseModel(buildTowerpulseLayout())
	if model.reading("tp-chiller-tower") == nil {
		t.Fatal("a healthy chiller reported nothing")
	}
	if err := model.DropChiller("tp-chiller-tower"); err != nil {
		t.Fatal(err)
	}
	if got := model.reading("tp-chiller-tower"); got != nil {
		t.Errorf("a dropped chiller reported %v", got)
	}
	if model.reading("tp-chiller-annex") == nil {
		t.Error("dropping one chiller silenced the other")
	}
	if err := model.DropChiller("tp-meter-tower"); err == nil {
		t.Error("DropChiller accepted a meter")
	}
	// A dropped chiller's building meter stops counting its power.
	tower := model.reading("tp-meter-tower")[TowerpulseMeterKwKey]
	model.advanceTo(30)
	if after := model.reading("tp-meter-tower")[TowerpulseMeterKwKey]; after >= tower {
		t.Errorf("the meter reads %v kW after the chiller dropped, was %v kW", after, tower)
	}
}

// The model is a pure function of elapsed time: two runs agree reading for reading, and the
// readings do not depend on how the time was sliced into ticks.
func TestTowerpulseModelIsDeterministicAndIndependentOfTickSpacing(t *testing.T) {
	a, _ := tpRun(t, nil, 900, 5)
	b, _ := tpRun(t, nil, 900, 5)
	if fmt.Sprint(a[TowerpulseHotZone]) != fmt.Sprint(b[TowerpulseHotZone]) {
		t.Error("two runs of the same model disagree")
	}
	c, _ := tpRun(t, nil, 900, 15)
	last := func(s []tpSample) tpSample { return s[len(s)-1] }
	if fmt.Sprint(last(a[TowerpulseHotZone]).m) != fmt.Sprint(last(c[TowerpulseHotZone]).m) {
		t.Errorf("tick spacing changed the reading at 900 s: %v vs %v", last(a[TowerpulseHotZone]).m, last(c[TowerpulseHotZone]).m)
	}
}
