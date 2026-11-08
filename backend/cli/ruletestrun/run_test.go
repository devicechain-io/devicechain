// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletestrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ruletest"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// fakeClient answers evaluateRules with a canned response and records what it was asked.
type fakeClient struct {
	calls     int
	endpoints []string
	inputs    []map[string]any
	response  string
	err       error
}

func (f *fakeClient) Query(_ context.Context, baseURL, _ string, vars map[string]any, out any) error {
	f.calls++
	f.endpoints = append(f.endpoints, baseURL)
	f.inputs = append(f.inputs, vars["input"].(map[string]any))
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(`{"evaluateRules":`+f.response+`}`), out)
}

const scenario = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
start: 2026-03-01T00:00:00Z
duration: 2h
devices:
  - token: d1
    streams:
      - {metric: temperature, every: 30m, constant: 90}
`

const tests = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: it raises
    raised: {rule: hot, device: d1, within: 5m}
  - name: it never clears
    neverRaised: {rule: cold}
  - name: it sends the fan command
    intent: {kind: sendCommand, command: set-fan, payload: {speed: 3}}
  - name: it clears
    cleared: {rule: hot}
`

const okResponse = `{"ok":true,"refusal":null,"evaluatedRules":["hot","cold"],"degraded":null,"degradations":[],
 "skippedRules":[{"rule":"grouped","reason":"GROUP_SCOPED","group":"north@2","message":"grouped: scoped to a group"}],"diagnostics":[],
 "edges":[{"occurredAt":"2026-03-01T00:00:00Z","series":"d1","rule":"hot","kind":"threshold","signal":"RAISED"}],
 "intents":[{"occurredAt":"2026-03-01T00:00:00Z","series":"d1","rule":"hot","signal":"RAISED","kind":"SEND_COMMAND",
   "alarmKey":null,"command":"set-fan","payload":"{\"speed\":3}","connector":null,"url":null,"body":null}]}`

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, files map[string]string) *ruletest.Plan {
	t.Helper()
	dir := t.TempDir()
	for n, b := range files {
		write(t, dir, n, b)
	}
	plan, err := ruletest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func byName(cases []ruletest.Case) map[string]ruletest.Case {
	m := map[string]ruletest.Case{}
	for _, c := range cases {
		m[c.Name] = c
	}
	return m
}

func TestRunJudgesEveryExpectationAndEvaluatesTheScenarioOnce(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": tests, "b.test.yaml": tests})
	fc := &fakeClient{response: okResponse}
	var out bytes.Buffer
	cases, err := Run(context.Background(), plan, Options{Client: fc, Endpoint: "http://ep/graphql", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if fc.calls != 1 || fc.endpoints[0] != "http://ep/graphql" {
		t.Fatalf("calls=%d endpoints=%v, want the shared scenario evaluated once at the endpoint", fc.calls, fc.endpoints)
	}
	if len(cases) != 8 {
		t.Fatalf("cases = %d, want 4 expectations x 2 files", len(cases))
	}
	passed, failed, errored := Summary(cases)
	if passed != 6 || failed != 2 || errored != 0 {
		t.Fatalf("summary = %d/%d/%d, want 6 passed, 2 failed (the two `it clears`)", passed, failed, errored)
	}
	for _, c := range cases {
		if c.Name == "it clears" && !strings.Contains(c.Failure, "never cleared") {
			t.Errorf("failure = %q", c.Failure)
		}
		if c.Name != "it clears" && !c.Passed() {
			t.Errorf("%s should pass: %+v", c.Name, c)
		}
	}
	report := out.String()
	for _, want := range []string{"evaluated ", "warning: a published rule was not evaluated: grouped", "PASS  ", "FAIL  "} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if got := Describe(passed, failed, errored); got != "6 passed, 2 failed, 0 errored" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestRunSendsTheScenarioAsTheEvaluationInput(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": tests})
	fc := &fakeClient{response: okResponse}
	if _, err := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"}); err != nil {
		t.Fatal(err)
	}
	in := fc.inputs[0]
	if in["profileToken"] != "boiler" || in["ruleSet"] != "PUBLISHED" || in["advanceTo"] != "2026-03-01T02:00:00Z" {
		t.Fatalf("input = %v", in)
	}
	events := in["events"].([]map[string]any)
	if len(events) != 5 { // 0, 30, 60, 90, 120 minutes
		t.Fatalf("events = %d", len(events))
	}
	if events[1]["deviceToken"] != "d1" || events[1]["occurredAt"] != "2026-03-01T00:30:00Z" {
		t.Fatalf("event = %v", events[1])
	}
	if _, ok := in["ruleDefinition"]; ok {
		t.Fatal("a published scenario must not send a draft")
	}
}

func TestRunSendsADraft(t *testing.T) {
	draft := strings.Replace(scenario, "profile: boiler\n", "profile: boiler\nruleSet: DRAFT\ndraft:\n  ruleDefinition: rule.json\n", 1)
	plan := load(t, map[string]string{"scenario.yaml": draft, "rule.json": `{"name":"hot"}`, "a.test.yaml": tests})
	fc := &fakeClient{response: okResponse}
	if _, err := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"}); err != nil {
		t.Fatal(err)
	}
	in := fc.inputs[0]
	if in["ruleSet"] != "DRAFT" || in["ruleDefinition"] != `{"name":"hot"}` {
		t.Fatalf("input = %v", in)
	}
	if _, ok := in["graph"]; ok {
		t.Fatal("a ruleDefinition draft must not also send a graph")
	}

	graphScenario := strings.Replace(draft, "ruleDefinition: rule.json", "graph: g.json", 1)
	plan = load(t, map[string]string{"scenario.yaml": graphScenario, "g.json": `{"schemaVersion":1}`, "a.test.yaml": tests})
	fc = &fakeClient{response: okResponse}
	if _, err := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"}); err != nil {
		t.Fatal(err)
	}
	if fc.inputs[0]["graph"] != `{"schemaVersion":1}` {
		t.Fatalf("input = %v", fc.inputs[0])
	}
}

func TestRunReportsRejectionTransportAndBrokenFilesAsErrors(t *testing.T) {
	// The platform rejected the scenario.
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": tests})
	fc := &fakeClient{response: `{"ok":false,"refusal":"NO_PUBLISHED_VERSION","evaluatedRules":[],"degraded":null,"degradations":[],"skippedRules":[],"edges":[],"intents":[],
	  "diagnostics":[{"severity":"error","message":"profile \"boiler\" has no published version","nodeId":null},
	                 {"severity":"error","message":"bad","nodeId":"n1"}]}`}
	cases, err := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].Error == "" || !strings.Contains(cases[0].Error, "has no published version; node n1: bad") {
		t.Fatalf("cases = %+v", cases)
	}
	if _, f, e := Summary(cases); f != 0 || e != 1 {
		t.Fatal("a rejection is an error, not a failed expectation")
	}

	// The server is unreachable.
	fc = &fakeClient{err: errors.New("connection refused")}
	cases, _ = Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"})
	if len(cases) != 1 || !strings.Contains(cases[0].Error, "connection refused") {
		t.Fatalf("cases = %+v", cases)
	}

	// One broken file does not hide the others.
	plan = load(t, map[string]string{"scenario.yaml": scenario, "good.test.yaml": tests,
		"bad.test.yaml": "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n  - raised: {rul: x}\n"})
	fc = &fakeClient{response: okResponse}
	cases, _ = Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"})
	passed, failed, errored := Summary(cases)
	if passed != 3 || failed != 1 || errored != 1 {
		t.Fatalf("summary = %d/%d/%d (cases %+v)", passed, failed, errored, cases)
	}
	if fc.calls != 1 {
		t.Fatalf("calls = %d; a broken file's scenario must not be evaluated twice", fc.calls)
	}
}

func TestRunDoesNotEvaluateAScenarioNoSuiteCanUse(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "orphan.yaml": scenario,
		"a.test.yaml": "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n  - raised: {rul: x}\n"})
	fc := &fakeClient{response: okResponse}
	cases, _ := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"})
	if fc.calls != 0 || len(cases) != 1 || cases[0].Error == "" {
		t.Fatalf("calls=%d cases=%+v", fc.calls, cases)
	}
}

// The server's body limit decides what is too large: a large scenario is sent, and the server's 413
// is reported with the knob that raises it.
func TestRunReportsTheServersBodyLimit(t *testing.T) {
	huge := strings.Replace(strings.Replace(scenario, "every: 30m", "every: 1s", 1), "duration: 2h", "duration: 12h", 1)
	plan := load(t, map[string]string{"scenario.yaml": huge, "a.test.yaml": tests})
	fc := &fakeClient{err: &userclient.StatusError{URL: "e", StatusCode: http.StatusRequestEntityTooLarge, Body: "too large"}}
	cases, _ := Run(context.Background(), plan, Options{Client: fc, Endpoint: "e"})
	if fc.calls != 1 || len(cases) != 1 || !strings.Contains(cases[0].Error, "DC_GRAPHQL_MAX_BODY_BYTES") {
		t.Fatalf("calls=%d cases=%+v, want the request sent and the 413 reported with the knob", fc.calls, cases)
	}
}

func TestRunStopsOnACancelledContext(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": tests})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fc := &fakeClient{response: okResponse}
	if _, err := Run(ctx, plan, Options{Client: fc, Endpoint: "e"}); !errors.Is(err, context.Canceled) || fc.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, fc.calls)
	}
}

func TestToResultRefusesAMalformedTime(t *testing.T) {
	var w wireResult
	w.Edges = append(w.Edges, struct {
		OccurredAt string `json:"occurredAt"`
		Series     string `json:"series"`
		Rule       string `json:"rule"`
		Kind       string `json:"kind"`
		Signal     string `json:"signal"`
	}{OccurredAt: "yesterday"})
	if _, err := toResult(w); err == nil || !strings.Contains(err.Error(), "edge time") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteJUnitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.xml")
	if err := WriteJUnit(path, []ruletest.Case{{Suite: "s", Name: "n"}, {Suite: "s", Name: "m", Failure: "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `<testsuite name="s" tests="2" failures="1"`) {
		t.Fatalf("report = %s (%v)", b, err)
	}
	if err := WriteJUnit(filepath.Join(t.TempDir(), "no", "such", "dir", "x.xml"), nil); err == nil {
		t.Fatal("an unwritable path must be an error")
	}
}

// busyThen answers BUSY for its first `busy` calls, then okResponse.
type busyThen struct {
	fakeClient
	busy int
}

const busyResponse = `{"ok":false,"refusal":"BUSY","evaluatedRules":[],"degraded":null,"degradations":[],"skippedRules":[],"edges":[],"intents":[],
  "diagnostics":[{"severity":"error","message":"already running","nodeId":null}]}`

func (b *busyThen) Query(ctx context.Context, url, q string, vars map[string]any, out any) error {
	b.response = okResponse
	if b.calls < b.busy {
		b.response = busyResponse
	}
	return b.fakeClient.Query(ctx, url, q, vars, out)
}

// A BUSY refusal is retried with a growing wait; one that outlasts every retry is an error.
func TestRunRetriesABusyPlatform(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": tests})
	var waits []time.Duration
	wait := func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }

	bc := &busyThen{busy: 2}
	cases, err := Run(context.Background(), plan, Options{Client: bc, Endpoint: "e", Wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	if bc.calls != 3 || len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("calls=%d waits=%v, want two retries after 1s and 2s", bc.calls, waits)
	}
	if _, _, errored := Summary(cases); errored != 0 {
		t.Fatalf("cases = %+v, want the retried evaluation judged", cases)
	}

	waits = nil
	bc = &busyThen{busy: 100}
	cases, _ = Run(context.Background(), plan, Options{Client: bc, Endpoint: "e", Wait: wait})
	if bc.calls != 1+len(busyBackoff) || len(cases) != 1 || !strings.Contains(cases[0].Error, "already running") {
		t.Fatalf("calls=%d cases=%+v, want every retry spent and the refusal reported", bc.calls, cases)
	}

	// Any other refusal is not retried.
	fc := &fakeClient{response: strings.Replace(busyResponse, `"BUSY"`, `"NO_RULES"`, 1)}
	waits = nil
	_, _ = Run(context.Background(), plan, Options{Client: fc, Endpoint: "e", Wait: wait})
	if fc.calls != 1 || len(waits) != 0 {
		t.Fatalf("calls=%d waits=%v, want a non-BUSY refusal reported at once", fc.calls, waits)
	}
}

// An expectation naming a rule the server did not evaluate is an error case, and so is an
// inconclusive one on a partial result; together they make the run exit 2.
func TestRunReportsUnjudgeableExpectationsAsErrors(t *testing.T) {
	plan := load(t, map[string]string{"scenario.yaml": scenario, "a.test.yaml": `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: a typo
    neverRaised: {rule: hott}
  - name: the skipped one
    raised: {rule: grouped}
  - name: never on a partial result
    neverRaised: {rule: cold}
`})
	partial := strings.Replace(okResponse, `"degradations":[]`, `"degradations":[{"reason":"LATE_READINGS","count":2}]`, 1)
	cases, err := Run(context.Background(), plan, Options{Client: &fakeClient{response: partial}, Endpoint: "e"})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(cases)
	if !strings.Contains(got["a typo"].Error, "rule hott was not evaluated") ||
		!strings.Contains(got["the skipped one"].Error, "rule grouped was skipped") ||
		!strings.Contains(got["never on a partial result"].Error, "inconclusive") {
		t.Fatalf("cases = %+v", cases)
	}
	_, failed, errored := Summary(cases)
	var ee *ExitError
	if err := Outcome(failed, errored); !errors.As(err, &ee) || ee.Code != ExitErrored {
		t.Fatalf("outcome = %v, want exit status 2", err)
	}
}

func TestToResultRefusesAnUnknownEnum(t *testing.T) {
	var w wireResult
	w.Edges = append(w.Edges, struct {
		OccurredAt string `json:"occurredAt"`
		Series     string `json:"series"`
		Rule       string `json:"rule"`
		Kind       string `json:"kind"`
		Signal     string `json:"signal"`
	}{OccurredAt: "2026-03-01T00:00:00Z", Signal: "raised"})
	if _, err := toResult(w); err == nil || !strings.Contains(err.Error(), "signal") {
		t.Fatalf("err = %v, want an unknown signal refused rather than read as resolved", err)
	}
}

// The exit status says which kind of not-green a run was: 1 a failed expectation, 2 a run that
// could not be judged, and an error outranks a failure.
func TestOutcomeExitStatus(t *testing.T) {
	if err := Outcome(0, 0); err != nil {
		t.Fatalf("all held: %v", err)
	}
	for _, c := range []struct{ failed, errored, want int }{{1, 0, 1}, {0, 1, 2}, {3, 1, 2}} {
		err := Outcome(c.failed, c.errored)
		var ee *ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != c.want {
			t.Fatalf("Outcome(%d, %d) = %v, want exit status %d", c.failed, c.errored, err, c.want)
		}
	}
}
