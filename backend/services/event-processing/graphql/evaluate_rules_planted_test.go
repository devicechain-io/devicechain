// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

// The planted-defect pack. A rule test is worth nothing until it has been shown to fail: this runs
// one scenario and one set of named expectations against the rule as it should be, and then against
// the same rule with one defect planted in it, through the real evaluateRules schema and the real
// scenario generator and judge `dcctl test` uses. Each defect must make exactly the expectations that
// name it fail, and the intact rule must make none fail.

const plantedScenario = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
ruleSet: DRAFT
draft:
  ruleDefinition: rule.json
start: 2026-03-01T00:00:00Z
duration: 2h
seed: 1
devices:
  - token: d1
    streams:
      - metric: temperature
        every: 10m
        steps:
          - {at: 0s, value: 20}
          - {at: 30m, value: 95}
          - {at: 1h30m, value: 20}
  - token: d2
    streams:
      - {metric: temperature, every: 10m, constant: 20}
`

const plantedTests = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: raised when the boiler first goes over 80
    raised: {rule: draft, device: d1, after: 25m, within: 10m}
  - name: cleared once it cools
    cleared: {rule: draft, device: d1, after: 85m, within: 10m}
  - name: a healthy device never raises
    neverRaised: {device: d2}
  - name: the fan is told to run at speed 3
    intent: {kind: sendCommand, command: set-fan, payload: {speed: 3}, device: d1, after: 25m, within: 10m}
  - name: the alarm is filed under overheat
    intent: {kind: raiseAlarm, alarmKey: overheat, device: d1, after: 25m, within: 10m}
  - name: the alarm is cleared with the same key
    intent: {kind: clearAlarm, alarmKey: overheat, device: d1, edge: resolved, after: 85m, within: 10m}
`

// plantedRule is the rule as it should be. Each defect replaces one fragment.
const plantedRule = `{"name":"hot","type":"threshold","severity":"critical",
 "when":{"metric":"temperature","op":"gt","threshold":80},
 "actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"overheat"}},
            {"type":"sendCommand","sendCommand":{"command":"set-fan","payload":"{\"speed\":3}"}}]}`

func runPlanted(t *testing.T, rule string) []ruletest.Outcome {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"scenario.yaml": plantedScenario, "rule.json": rule, "boiler.test.yaml": plantedTests} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := ruletest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	suite := plan.Suites[0]
	if suite.Err != nil {
		t.Fatal(suite.Err)
	}
	sc := plan.Scenarios[suite.ScenarioPath]
	if sc.Err != nil {
		t.Fatal(sc.Err)
	}
	events := make([]any, len(sc.Events.Events))
	for i, e := range sc.Events.Events {
		ms := make([]any, len(e.Metrics))
		for j, m := range e.Metrics {
			ms[j] = map[string]any{"name": m.Name, "value": m.Value}
		}
		events[i] = map[string]any{"deviceToken": e.Device, "occurredAt": e.At.UTC().Format(time.RFC3339Nano), "measurements": ms}
	}
	in := map[string]any{"profileToken": sc.Scenario.Profile, "ruleSet": "DRAFT", "ruleDefinition": sc.Draft,
		"events": events, "advanceTo": sc.Events.AdvanceTo.UTC().Format(time.RFC3339Nano)}
	got, errs := evExec(t, evalSchema(t, &SchemaResolver{}), previewCtx(auth.DeviceRead), in)
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if !got.Ok {
		t.Fatalf("the evaluation was rejected: %+v", got.Diagnostics)
	}
	res := ruletest.Result{Evaluated: got.EvaluatedRules}
	if got.Degraded != nil {
		res.Degraded = *got.Degraded
	}
	for _, d := range got.Degradations {
		res.Degradations = append(res.Degradations, ruletest.Degradation{Reason: d.Reason, Count: d.Count})
	}
	for _, e := range got.Edges {
		at, _ := time.Parse(time.RFC3339Nano, e.OccurredAt)
		res.Edges = append(res.Edges, ruletest.Edge{At: at, Series: e.Series, Rule: e.Rule, Kind: e.Kind, Raise: e.Signal == "RAISED"})
	}
	for _, i := range got.Intents {
		at, _ := time.Parse(time.RFC3339Nano, i.OccurredAt)
		in := ruletest.Intent{At: at, Series: i.Series, Rule: i.Rule, Raise: i.Signal == "RAISED", Kind: wireIntentKinds[i.Kind]}
		if i.AlarmKey != nil {
			in.AlarmKey = *i.AlarmKey
		}
		if i.Command != nil {
			in.Command = *i.Command
		}
		if i.Payload != nil {
			in.Payload = *i.Payload
		}
		res.Intents = append(res.Intents, in)
	}
	return ruletest.Judge(suite.Test, res, sc.Scenario.Start, sc.Scenario.Duration.D())
}

func failing(outcomes []ruletest.Outcome) []string {
	var names []string
	for _, o := range outcomes {
		if !o.Passed {
			names = append(names, o.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestPlantedDefectsEachFailTheirNamedExpectations(t *testing.T) {
	if f := failing(runPlanted(t, plantedRule)); len(f) != 0 {
		t.Fatalf("the intact rule must pass every expectation, but these failed: %v", f)
	}

	const (
		raised   = "raised when the boiler first goes over 80"
		cleared  = "cleared once it cools"
		healthy  = "a healthy device never raises"
		fan      = "the fan is told to run at speed 3"
		alarm    = "the alarm is filed under overheat"
		clearKey = "the alarm is cleared with the same key"
	)
	defects := []struct {
		name string
		rule string
		fail []string
	}{
		{
			// The threshold is wired to 98: 95 never crosses it, so nothing is raised and nothing
			// follows from a raise. The clear is a clear of nothing.
			name: "miswired threshold",
			rule: strings.Replace(plantedRule, `"threshold":80`, `"threshold":98`, 1),
			fail: []string{raised, fan, alarm, clearKey, cleared},
		},
		{
			// The command key is a typo. The alarm and the edges are untouched, so only the
			// expectation that names the command fails.
			name: "wrong command key",
			rule: strings.Replace(plantedRule, `"command":"set-fan"`, `"command":"set-fam"`, 1),
			fail: []string{fan},
		},
		{
			// The comparison is inverted: the rule raises while the boiler is cold and clears while
			// it is hot, which is the opposite of the alarm it is meant to be.
			name: "inverted comparison",
			rule: strings.Replace(plantedRule, `"op":"gt"`, `"op":"lt"`, 1),
			fail: []string{raised, cleared, healthy, fan, alarm, clearKey},
		},
		{
			// The alarm is filed under a different key on both edges. Raising and clearing agree
			// with each other, and disagree with what the test says the operator sees.
			name: "wrong alarm key",
			rule: strings.Replace(plantedRule, `"alarmKey":"overheat"`, `"alarmKey":"overheating"`, 1),
			fail: []string{alarm, clearKey},
		},
		{
			// The command's arguments are wrong: the right command at the wrong speed.
			name: "wrong command argument",
			rule: strings.Replace(plantedRule, `\"speed\":3`, `\"speed\":9`, 1),
			fail: []string{fan},
		},
	}
	for _, d := range defects {
		t.Run(d.name, func(t *testing.T) {
			if d.rule == plantedRule {
				t.Fatal("the defect was not planted: the rule is unchanged")
			}
			got := failing(runPlanted(t, d.rule))
			want := append([]string(nil), d.fail...)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("failed expectations:\n  got  %q\n  want %q", got, want)
			}
		})
	}
}

// wireIntentKinds maps the served EvaluatedIntentKind to the test file's vocabulary, as dcctl does.
var wireIntentKinds = map[string]string{
	"RAISE_ALARM": "raiseAlarm", "CLEAR_ALARM": "clearAlarm", "SEND_COMMAND": "sendCommand",
	"HTTP_CALL": "httpCall", "PUBLISH": "publish",
}
