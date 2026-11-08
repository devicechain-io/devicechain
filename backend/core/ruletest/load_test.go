// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const okTest = "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n  - raised: {rule: hot}\n"

func TestLoadSharesOneScenarioAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenario.yaml", baseScenario)
	write(t, dir, "a.test.yaml", okTest)
	write(t, dir, "sub/b.test.yaml", okTest+"")
	write(t, dir, "sub/scenario.yaml", baseScenario)
	write(t, dir, "notes.yaml", "ignored: true")
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Suites) != 2 || len(plan.Scenarios) != 2 {
		t.Fatalf("suites=%d scenarios=%d, want 2 and 2 (each directory has its own scenario)", len(plan.Suites), len(plan.Scenarios))
	}
	for _, s := range plan.Suites {
		if s.Err != nil || s.Test == nil {
			t.Fatalf("suite %s: %v", s.Path, s.Err)
		}
		if l := plan.Scenarios[s.ScenarioPath]; l == nil || l.Events == nil || len(l.Events.Events) != 13 {
			t.Fatalf("scenario for %s not generated: %+v", s.Path, l)
		}
	}
}

func TestLoadNamedScenarioAndDraft(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenarios/s.yaml", strings.Replace(baseScenario, "profile: boiler\n",
		"profile: boiler\nruleSet: DRAFT\ndraft:\n  ruleDefinition: rule.json\n", 1))
	write(t, dir, "scenarios/rule.json", `{"name":"r"}`)
	write(t, dir, "t.test.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nscenario: scenarios/s.yaml\nname: drafty\nexpect:\n  - raised: {}\n")
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := plan.Suites[0]
	if s.Err != nil {
		t.Fatal(s.Err)
	}
	if !strings.HasPrefix(s.Name, "drafty (") {
		t.Fatalf("name = %q", s.Name)
	}
	if l := plan.Scenarios[s.ScenarioPath]; l.Draft != `{"name":"r"}` {
		t.Fatalf("draft = %q", l.Draft)
	}
}

func TestLoadRecordsBrokenFilesInsteadOfStopping(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenario.yaml", baseScenario)
	write(t, dir, "good.test.yaml", okTest)
	write(t, dir, "bad.test.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n  - raised: {rul: x}\n")
	write(t, dir, "esc.test.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nscenario: ../outside.yaml\nexpect:\n  - raised: {}\n")
	write(t, dir, "missing.test.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nscenario: nope.yaml\nexpect:\n  - raised: {}\n")
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Suite{}
	for _, s := range plan.Suites {
		byName[filepath.Base(s.Path)] = s
	}
	if byName["good.test.yaml"].Err != nil {
		t.Fatalf("good: %v", byName["good.test.yaml"].Err)
	}
	for name, want := range map[string]string{
		"bad.test.yaml":     "rul",
		"esc.test.yaml":     "leaves the directory",
		"missing.test.yaml": "does not exist",
	} {
		if err := byName[name].Err; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

func TestLoadScenarioErrorIsReportedOnEverySuiteThatUsesIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenario.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: Scenario\n")
	write(t, dir, "a.test.yaml", okTest)
	write(t, dir, "b.test.yaml", okTest)
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Suites {
		if s.Err == nil || !strings.Contains(s.Err.Error(), "profile is required") {
			t.Fatalf("%s: err = %v", s.Path, s.Err)
		}
	}
}

func TestLoadRefusesNothingToRun(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenario.yaml", baseScenario)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "no .test.yaml file") {
		t.Fatalf("err = %v, want a refusal: a run that checks nothing must not pass", err)
	}
	if _, err := Load(filepath.Join(dir, "scenario.yaml")); err == nil || !strings.Contains(err.Error(), "not a test file") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("a missing path must fail")
	}
}

func TestLoadSingleFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scenario.yaml", baseScenario)
	p := write(t, dir, "one.test.yaml", okTest)
	plan, err := Load(p)
	if err != nil || len(plan.Suites) != 1 || plan.Suites[0].Err != nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestPlaybackCSVAndNDJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "rec.csv", "at,value\n0m,10\n2026-01-01T00:30:00Z,20\n90m,30\n")
	write(t, dir, "rec.ndjson", "{\"at\":\"15m\",\"value\":1.5}\n\n{\"at\":\"45m\",\"value\":2.5,\"metric\":\"humidity\"}\n")
	scenario := `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
start: 2026-01-01T00:00:00Z
duration: 2h
devices:
  - token: d1
    streams:
      - {metric: temperature, playback: {file: rec.csv}}
      - {metric: temperature, playback: {file: rec.ndjson}}
`
	write(t, dir, "scenario.yaml", scenario)
	write(t, dir, "t.test.yaml", okTest)
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := plan.Scenarios[plan.Suites[0].ScenarioPath]
	if l.Err != nil {
		t.Fatal(l.Err)
	}
	var got []string
	for _, e := range l.Events.Events {
		got = append(got, e.At.Sub(start).String()+"/"+e.Metrics[0].Name)
	}
	want := []string{"0s/temperature", "15m0s/temperature", "30m0s/temperature", "45m0s/humidity", "1h30m0s/temperature"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func TestPlaybackRefusals(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"no header":      {"r.csv", "", "the file is empty"},
		"unknown column": {"r.csv", "at,value,color\n1m,1,red\n", "unknown column"},
		"missing value":  {"r.csv", "at\n1m\n", `no "value" column`},
		"dup column":     {"r.csv", "at,at,value\n", "twice"},
		"bad number":     {"r.csv", "at,value\n1m,hot\n", "not a number"},
		"nan":            {"r.csv", "at,value\n1m,NaN\n", "finite"},
		"bad at":         {"r.csv", "at,value\nyesterday,1\n", "neither an RFC 3339"},
		"outside":        {"r.csv", "at,value\n3h,1\n", "outside the scenario"},
		"before":         {"r.csv", "at,value\n-1m,1\n", "outside the scenario"},
		"other device":   {"r.csv", "at,value,device\n1m,1,d9\n", "played on device"},
		"bad extension":  {"r.txt", "at,value\n", "must end in"},
		"ndjson unknown": {"r.ndjson", "{\"at\":\"1m\",\"value\":1,\"x\":2}\n", "unknown field"},
		"ndjson missing": {"r.ndjson", "{\"at\":\"1m\"}\n", "needs at and value"},
		"ndjson bad":     {"r.ndjson", "not json\n", "line 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.file, c.body)
			write(t, dir, "scenario.yaml", strings.Replace(baseScenario, "      - metric: temperature\n        every: 10m\n        constant: 21.5\n",
				"      - {metric: temperature, playback: {file: "+c.file+"}}\n", 1))
			write(t, dir, "t.test.yaml", okTest)
			plan, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			l := plan.Scenarios[plan.Suites[0].ScenarioPath]
			if l.Err == nil || !strings.Contains(l.Err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", l.Err, c.want)
			}
		})
	}
}

func TestPlaybackCannotLeaveTheScenarioDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "secret.csv", "at,value\n1m,1\n")
	write(t, dir, "sub/scenario.yaml", strings.Replace(baseScenario, "      - metric: temperature\n        every: 10m\n        constant: 21.5\n",
		"      - {metric: temperature, playback: {file: ../secret.csv}}\n", 1))
	write(t, dir, "sub/t.test.yaml", okTest)
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := plan.Scenarios[plan.Suites[0].ScenarioPath]
	if l.Err == nil || !strings.Contains(l.Err.Error(), "leaves the directory") {
		t.Fatalf("err = %v", l.Err)
	}
}

func TestJUnit(t *testing.T) {
	cases := []Case{
		{Suite: "b.test.yaml", Name: "ok one", Took: 1500 * time.Millisecond},
		{Suite: "b.test.yaml", Name: "bad one", Failure: "expected X\nsecond line"},
		{Suite: "a.test.yaml", Name: "broken", Error: "server unreachable <&>"},
		{Suite: "a.test.yaml", Name: "fine"},
	}
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, cases); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
		Errors   int `xml:"errors,attr"`
		Suites   []struct {
			Name     string `xml:"name,attr"`
			Tests    int    `xml:"tests,attr"`
			Failures int    `xml:"failures,attr"`
			Errors   int    `xml:"errors,attr"`
			Time     string `xml:"time,attr"`
			Cases    []struct {
				Name    string `xml:"name,attr"`
				Failure *struct {
					Message string `xml:"message,attr"`
					Text    string `xml:",chardata"`
				} `xml:"failure"`
				Error *struct {
					Text string `xml:",chardata"`
				} `xml:"error"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, buf.String())
	}
	if doc.Tests != 4 || doc.Failures != 1 || doc.Errors != 1 || len(doc.Suites) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Suites[0].Name != "a.test.yaml" || doc.Suites[0].Errors != 1 || doc.Suites[0].Cases[0].Error.Text != "server unreachable <&>" {
		t.Fatalf("suite a = %+v", doc.Suites[0])
	}
	b := doc.Suites[1]
	if b.Name != "b.test.yaml" || b.Failures != 1 || b.Time != "1.500" || b.Cases[1].Failure.Message != "expected X" ||
		b.Cases[1].Failure.Text != "expected X\nsecond line" {
		t.Fatalf("suite b = %+v", b)
	}
	var again bytes.Buffer
	_ = WriteJUnit(&again, cases)
	if !bytes.Equal(buf.Bytes(), again.Bytes()) {
		t.Fatal("the report is not a pure function of its cases")
	}
	if !(Case{}).Passed() || (Case{Failure: "x"}).Passed() || (Case{Error: "x"}).Passed() {
		t.Fatal("Passed is wrong")
	}
}

// A playback stream honours its from and until like a generated one: `until` is how a recorded
// device goes quiet, and a row after it must not be sent.
func TestPlaybackHonoursFromAndUntil(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "rec.csv", "at,value\n0m,10\n20m,20\n40m,30\n60m,40\n90m,50\n")
	write(t, dir, "scenario.yaml", `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
start: 2026-01-01T00:00:00Z
duration: 2h
devices:
  - token: d1
    streams:
      - {metric: temperature, from: 20m, until: 1h, playback: {file: rec.csv}}
`)
	write(t, dir, "t.test.yaml", okTest)
	plan, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := plan.Scenarios[plan.Suites[0].ScenarioPath]
	if l.Err != nil {
		t.Fatal(l.Err)
	}
	var got []string
	for _, e := range l.Events.Events {
		got = append(got, e.At.Sub(start).String())
	}
	if want := "20m0s,40m0s,1h0m0s"; strings.Join(got, ",") != want {
		t.Fatalf("events at %v, want %s (the rows inside [from, until], both ends included)", got, want)
	}
}
