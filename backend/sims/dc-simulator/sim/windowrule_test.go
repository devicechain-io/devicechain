// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validAggregate() AggregateAlarmRule {
	return AggregateAlarmRule{
		Token: "r", Name: "n", Metric: "temp", Op: OpGt, Threshold: 26, Window: 5 * time.Minute,
		Severity: SeverityMajor, AlarmKey: "k", Enabled: true,
	}
}

func decodeAny(t *testing.T, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The aggregate rule renders exactly the keys event-processing's rules.Rule decodes (it decodes
// with DisallowUnknownFields, so a stray or misspelled key is a profile that will not publish).
// The authored-rules fixture hands the same bytes to the real compiler; this pins the shape on
// this side, key by key.
func TestAggregateAlarmRuleRendersTheRuleDocument(t *testing.T) {
	r := validAggregate()
	r.GateMetric, r.GateOp, r.GateThreshold = "occupancy", OpGt, 0
	r.CommandKey = "boost"
	r.CommandPayload = map[string]any{"minutes": 15}
	r.CommandExpiresAfter = "30m"
	doc := decodeAny(t, r.DefinitionJSON())

	want := map[string]any{
		"name": "n", "type": "aggregate", "severity": "major", "windowMode": "sliding", "agg": "avg",
		"metric": "temp", "op": "gt", "threshold": 26.0, "window": "5m0s",
		"when": map[string]any{"metric": "occupancy", "op": "gt", "threshold": 0.0},
		"actions": []any{
			map[string]any{"type": "raiseAlarm", "raiseAlarm": map[string]any{"alarmKey": "k"}},
			map[string]any{"type": "sendCommand", "sendCommand": map[string]any{
				"command": "boost", "expiresAfter": "30m", "payload": `{"minutes":15}`}},
		},
	}
	if !reflect.DeepEqual(doc, want) {
		t.Errorf("document = %#v\nwant       %#v", doc, want)
	}
}

// A rule without a gate or a command renders neither: an empty `when` or `payload` key would be a
// document the byte-compared fixtures never had, and an empty payload string is ACCEPTED by the
// publish gate as absent, so it would look configured and carry nothing.
func TestAggregateAlarmRuleOmitsWhatItDoesNotHave(t *testing.T) {
	doc := decodeAny(t, validAggregate().DefinitionJSON())
	if _, has := doc["when"]; has {
		t.Error("an ungated rule renders a `when`")
	}
	if n := len(doc["actions"].([]any)); n != 1 {
		t.Errorf("%d actions, want only the alarm", n)
	}
	r := validAggregate()
	r.CommandKey = "boost"
	send := decodeAny(t, r.DefinitionJSON())["actions"].([]any)[1].(map[string]any)["sendCommand"].(map[string]any)
	if _, has := send["payload"]; has {
		t.Error("a command with no arguments renders a payload key")
	}
	if _, has := send["expiresAfter"]; has {
		t.Error("a command with no expiry renders an expiresAfter key")
	}
}

// Required fields have no default, so a wrong direction cannot hide behind one.
func TestAggregateAlarmRuleRefusesWhatItCannotRenderSafely(t *testing.T) {
	for name, mutate := range map[string]func(*AggregateAlarmRule){
		"no operator":     func(r *AggregateAlarmRule) { r.Op = "" },
		"unknown op":      func(r *AggregateAlarmRule) { r.Op = "ge" },
		"no window":       func(r *AggregateAlarmRule) { r.Window = 0 },
		"negative window": func(r *AggregateAlarmRule) { r.Window = -time.Minute },
		"gate with no op": func(r *AggregateAlarmRule) { r.GateMetric = "occupancy" },
	} {
		t.Run(name, func(t *testing.T) {
			r := validAggregate()
			mutate(&r)
			defer func() {
				if recover() == nil {
					t.Error("rendered a rule that would be silently wrong")
				}
			}()
			r.DefinitionJSON()
		})
	}
}

func TestAbsenceAlarmRule(t *testing.T) {
	r := AbsenceAlarmRule{Token: "a", Name: "n", Metric: "power_kw", Timeout: 2 * time.Minute,
		Severity: SeverityCritical, AlarmKey: "gone", Enabled: true}
	doc := decodeAny(t, r.DefinitionJSON())
	if doc["type"] != "absence" || doc["timeout"] != "2m0s" || doc["severity"] != "critical" {
		t.Errorf("document = %v", doc)
	}
	// Absence takes no leaf, no window and no value: event-processing refuses any of them.
	for _, forbidden := range []string{"metric", "op", "threshold", "window", "windowMode", "agg"} {
		if _, has := doc[forbidden]; has {
			t.Errorf("an absence rule renders %q, which the compiler refuses on this kind", forbidden)
		}
	}
	if w := doc["when"].(map[string]any); len(w) != 0 {
		t.Errorf("an absence rule carries a gate: %v", w)
	}
	if spec := r.Spec(); spec.Metric != "power_kw" || !spec.Enabled || spec.Definition != r.DefinitionJSON() {
		t.Errorf("spec = %+v", spec)
	}
	defer func() {
		if recover() == nil {
			t.Error("an absence rule with no timeout rendered")
		}
	}()
	AbsenceAlarmRule{Token: "a"}.DefinitionJSON()
}

// Validate reads BOTH metrics of a windowed rule, and the declared metric may be either.
func TestRuleReadMetricsReadsTheValueAndTheGate(t *testing.T) {
	r := validAggregate()
	r.GateMetric, r.GateOp = "occupancy", OpGt
	for _, tc := range []struct {
		name, doc string
		want      []string
	}{
		{"windowed with a gate", r.DefinitionJSON(), []string{"temp", "occupancy"}},
		{"windowed, no gate", validAggregate().DefinitionJSON(), []string{"temp"}},
		{"a threshold rule", `{"type":"threshold","when":{"metric":"m","op":"gt","threshold":1}}`, []string{"m"}},
		{"the same metric twice is read once", `{"metric":"m","when":{"metric":"m"}}`, []string{"m"}},
		{"nothing to read", `{"type":"absence","when":{}}`, nil},
		{"not JSON", `{nope`, nil},
	} {
		if got := ruleReadMetrics(tc.doc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A rule declaring one metric while reading another is still refused, now with two metrics in the
// reading: the declared one must be among them.
func TestValidateStillRefusesADeclaredMetricTheRuleDoesNotRead(t *testing.T) {
	m := towerpulseManifest(t)
	for i, p := range m.Profiles {
		if p.Token != TowerpulseVAVProfileToken {
			continue
		}
		spec := validAggregate().Spec()
		spec.Token, spec.Metric = "tp-rule-liar", TowerpulseCO2Key
		spec.Definition = strings.ReplaceAll(spec.Definition, `"temp"`, `"`+TowerpulseTempKey+`"`)
		m.Profiles[i].DetectionRules = append(append([]DetectionRuleSpec(nil), p.DetectionRules...), spec)
	}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "declares metric") {
		t.Errorf("Validate = %v, want a refusal that the declared metric is not the one read", err)
	}
}

// Named populations: a population states its devices one way.
func TestPopulationsThatNameTheirDevicesAreValidatedStrictly(t *testing.T) {
	base := towerpulseManifest(t)
	for name, mutate := range map[string]func(*PopulationSpec){
		"count disagrees":       func(p *PopulationSpec) { p.Count++ },
		"also numbered":         func(p *PopulationSpec) { p.TokenPattern = "x-{n}" },
		"also distributed":      func(p *PopulationSpec) { p.DistributeAcross = []string{"area"} },
		"a token named twice":   func(p *PopulationSpec) { p.Instances[1].Token = p.Instances[0].Token },
		"a non-finite constant": func(p *PopulationSpec) { p.Instances[0].Constants = map[string]float64{"a": nan()} },
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			m.Populations = append([]PopulationSpec(nil), base.Populations...)
			p := m.Populations[0]
			p.Instances = append([]InstanceSpec(nil), p.Instances...)
			mutate(&p)
			m.Populations[0] = p
			if err := m.Validate(); err == nil {
				t.Error("an inconsistent named population validated")
			}
		})
	}
}

func nan() float64 { var z float64; return z / z }
