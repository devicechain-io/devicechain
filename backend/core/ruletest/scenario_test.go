// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestParseDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"90m":    90 * time.Minute,
		"7d":     7 * 24 * time.Hour,
		"1w":     7 * 24 * time.Hour,
		"1w2d":   9 * 24 * time.Hour,
		"1d12h":  36 * time.Hour,
		"-5m":    -5 * time.Minute,
		"1.5s":   1500 * time.Millisecond,
		" 30s ":  30 * time.Second,
		"2d0s":   48 * time.Hour,
		"500ms":  500 * time.Millisecond,
		"0s":     0,
		"36500d": 36500 * 24 * time.Hour,
	}
	for in, want := range ok {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "5", "d", "7x", "abc", "99999999999999d", "9999w"} {
		if got, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want an error", bad, got)
		}
	}
}

const baseScenario = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
start: 2026-01-01T00:00:00Z
duration: 2h
seed: 7
devices:
  - token: d1
    streams:
      - metric: temperature
        every: 10m
        constant: 21.5
`

func mustScenario(t *testing.T, y string) *Scenario {
	t.Helper()
	s, err := ParseScenario([]byte(y))
	if err != nil {
		t.Fatalf("ParseScenario: %v", err)
	}
	return s
}

func gen(t *testing.T, s *Scenario) *Generated {
	t.Helper()
	g, err := s.Generate(func(string) ([]byte, error) { return nil, errors.New("no files in this test") })
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return g
}

func TestParseScenarioRefusals(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"unknown key":        {baseScenario + "bogus: 1\n", "bogus"},
		"two documents":      {baseScenario + "---\nfoo: bar\n", "more than one"},
		"empty":              {"", "empty"},
		"wrong version":      {strings.Replace(baseScenario, "v1alpha1", "v9", 1), "not one this build reads"},
		"no version":         {strings.Replace(baseScenario, "apiVersion: devicechain.io/ruletest/v1alpha1\n", "", 1), "apiVersion is required"},
		"wrong kind":         {strings.Replace(baseScenario, "kind: Scenario", "kind: RuleTest", 1), "kind must be Scenario"},
		"no profile":         {strings.Replace(baseScenario, "profile: boiler\n", "", 1), "profile is required"},
		"no start":           {strings.Replace(baseScenario, "start: 2026-01-01T00:00:00Z\n", "", 1), "start is required"},
		"no duration":        {strings.Replace(baseScenario, "duration: 2h\n", "", 1), "duration is required"},
		"bare number":        {strings.Replace(baseScenario, "duration: 2h", "duration: 7200", 1), "invalid duration"},
		"no devices":         {"apiVersion: devicechain.io/ruletest/v1alpha1\nkind: Scenario\nprofile: p\nstart: 2026-01-01T00:00:00Z\nduration: 1h\n", "devices is required"},
		"bad ruleset":        {baseScenario + "ruleSet: LIVE\n", "ruleSet must be"},
		"draft sans ruleset": {baseScenario + "draft:\n  graph: g.json\n", "set ruleSet: DRAFT"},
		"draft sans draft":   {baseScenario + "ruleSet: DRAFT\n", "needs a draft"},
		"draft both":         {baseScenario + "ruleSet: DRAFT\ndraft:\n  graph: g.json\n  ruleDefinition: r.json\n", "exactly one"},
		"dup device":         {baseScenario + "  - token: d1\n    streams: []\n", "listed twice"},
		"no behaviour":       {strings.Replace(baseScenario, "        constant: 21.5\n", "", 1), "exactly one behaviour"},
		"two behaviours":     {baseScenario + "        steps:\n          - {at: 0s, value: 1}\n", "exactly one behaviour"},
		"no every":           {strings.Replace(baseScenario, "        every: 10m\n", "", 1), "every is required"},
		"no metric":          {strings.Replace(baseScenario, "      - metric: temperature\n        every", "      - every", 1), "metric is required"},
		"until past end":     {baseScenario + "        until: 3h\n", "past the scenario's duration"},
		"until before from":  {baseScenario + "        from: 1h\n        until: 30m\n", "until must be after from"},
		"walk bounds":        {strings.Replace(baseScenario, "constant: 21.5", "randomWalk: {start: 5, step: 1, min: 10, max: 20}", 1), "start is outside"},
		"walk min max":       {strings.Replace(baseScenario, "constant: 21.5", "randomWalk: {start: 15, step: 1, min: 20, max: 10}", 1), "min is above max"},
		"steps order":        {strings.Replace(baseScenario, "constant: 21.5", "steps: [{at: 0s, value: 1}, {at: 0s, value: 2}]", 1), "must increase"},
		"steps first":        {strings.Replace(baseScenario, "constant: 21.5", "steps: [{at: 5m, value: 1}]", 1), "first step must be at 0s"},
		"fault none":         {baseScenario + "faults:\n  - {}\n", "exactly one of gap"},
		"fault two":          {baseScenario + "faults:\n  - gap: {from: 0s, for: 1m}\n    offline: {from: 0s, for: 1m}\n", "exactly one of gap"},
		"fault device":       {baseScenario + "faults:\n  - gap: {device: nope, from: 0s, for: 1m}\n", "not in the scenario"},
		"gap length":         {baseScenario + "faults:\n  - gap: {from: 0s, for: 0s}\n", "for must be positive"},
		"dup probability":    {baseScenario + "faults:\n  - duplicate: {probability: 1.5}\n", "probability must be in"},
		"reorder delay":      {baseScenario + "faults:\n  - reorder: {probability: 0.5}\n", "maxDelay is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseScenario([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestConstantGeneratesExactlyTheSchedule(t *testing.T) {
	g := gen(t, mustScenario(t, baseScenario))
	if len(g.Events) != 13 { // 0m .. 120m every 10m, both ends included
		t.Fatalf("events = %d, want 13", len(g.Events))
	}
	for i, e := range g.Events {
		if !e.At.Equal(start.Add(time.Duration(i)*10*time.Minute)) || e.Device != "d1" ||
			len(e.Metrics) != 1 || e.Metrics[0] != (Metric{"temperature", 21.5}) {
			t.Fatalf("event %d = %+v", i, e)
		}
	}
	if !g.AdvanceTo.Equal(start.Add(2 * time.Hour)) {
		t.Fatalf("advanceTo = %v", g.AdvanceTo)
	}
}

func TestFromAndUntilBoundAStream(t *testing.T) {
	y := baseScenario + "        from: 30m\n        until: 1h\n"
	g := gen(t, mustScenario(t, y))
	if len(g.Events) != 4 || !g.Events[0].At.Equal(start.Add(30*time.Minute)) || !g.Events[3].At.Equal(start.Add(time.Hour)) {
		t.Fatalf("events = %+v, want samples at 30, 40, 50, 60m", g.Events)
	}
}

func TestStepsHoldUntilTheNextStep(t *testing.T) {
	y := strings.Replace(baseScenario, "constant: 21.5", "steps: [{at: 0s, value: 20}, {at: 30m, value: 95}, {at: 1h30m, value: 20}]", 1)
	g := gen(t, mustScenario(t, y))
	var got []float64
	for _, e := range g.Events {
		got = append(got, e.Metrics[0].Value)
	}
	want := []float64{20, 20, 20, 95, 95, 95, 95, 95, 95, 20, 20, 20, 20}
	// index 3 is 30m (the step is in effect AT its instant), index 9 is 90m
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
}

func walkScenario(extra string) string {
	return strings.Replace(baseScenario, "constant: 21.5", "randomWalk: {start: 20, step: 3, min: 15, max: 25}", 1) + extra
}

func TestRandomWalkIsBoundedSeededAndIndependentOfOtherDevices(t *testing.T) {
	y := strings.Replace(walkScenario(""), "duration: 2h", "duration: 48h", 1)
	a := gen(t, mustScenario(t, y))
	if len(a.Events) < 100 {
		t.Fatalf("too few samples to prove a bound: %d", len(a.Events))
	}
	moved := false
	for _, e := range a.Events {
		v := e.Metrics[0].Value
		if v < 15 || v > 25 {
			t.Fatalf("walk left its bounds: %v", v)
		}
		if v != 20 {
			moved = true
		}
	}
	if !moved {
		t.Fatal("the walk never moved")
	}
	// Same seed, same walk.
	b := gen(t, mustScenario(t, y))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same seed generated different events")
	}
	// A different seed, a different walk.
	c := gen(t, mustScenario(t, strings.Replace(y, "seed: 7", "seed: 8", 1)))
	if reflect.DeepEqual(a.Events, c.Events) {
		t.Fatal("a different seed generated the same walk")
	}
	// Adding another device does not change d1's walk: each stream draws from its own generator.
	more := y + "  - token: d0\n    streams:\n      - {metric: temperature, every: 10m, randomWalk: {start: 20, step: 3, min: 15, max: 25}}\n"
	d := gen(t, mustScenario(t, more))
	var d1 []Event
	for _, e := range d.Events {
		if e.Device == "d1" {
			d1 = append(d1, e)
		}
	}
	if !reflect.DeepEqual(a.Events, d1) {
		t.Fatal("adding a device changed another device's readings")
	}
}

func TestReadingsOfOneInstantAreOneEvent(t *testing.T) {
	y := baseScenario + "      - {metric: humidity, every: 10m, constant: 40}\n"
	g := gen(t, mustScenario(t, y))
	if len(g.Events) != 13 {
		t.Fatalf("events = %d, want 13 (two metrics sampled together are one event)", len(g.Events))
	}
	if len(g.Events[0].Metrics) != 2 {
		t.Fatalf("metrics = %+v", g.Events[0].Metrics)
	}
}

func TestGapDropsTheWindow(t *testing.T) {
	g := gen(t, mustScenario(t, baseScenario+"faults:\n  - gap: {device: d1, from: 30m, for: 30m}\n"))
	for _, e := range g.Events {
		if off := e.At.Sub(start); off >= 30*time.Minute && off < time.Hour {
			t.Fatalf("a sample at +%s survived the gap", off)
		}
	}
	if len(g.Events) != 10 { // 13 - samples at 30, 40, 50
		t.Fatalf("events = %d, want 10", len(g.Events))
	}
}

func TestGapCanTargetOneMetric(t *testing.T) {
	y := baseScenario + "      - {metric: humidity, every: 10m, constant: 40}\nfaults:\n  - gap: {metric: humidity, from: 0s, for: 3h}\n"
	g := gen(t, mustScenario(t, y))
	for _, e := range g.Events {
		if len(e.Metrics) != 1 || e.Metrics[0].Name != "temperature" {
			t.Fatalf("event = %+v, want temperature only", e)
		}
	}
}

func TestOfflineDeliversTheWindowLate(t *testing.T) {
	g := gen(t, mustScenario(t, baseScenario+"faults:\n  - offline: {device: d1, from: 30m, for: 30m}\n"))
	if len(g.Events) != 13 {
		t.Fatalf("events = %d, want all 13 (offline delays, it does not drop)", len(g.Events))
	}
	// Delivery order: 0, 10, 20, then the samples from 60m on, with 30/40/50 landing at 60m.
	var order []time.Duration
	for _, e := range g.Events {
		order = append(order, e.At.Sub(start))
	}
	want := []time.Duration{0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120}
	for i := range want {
		want[i] *= time.Minute
	}
	// The buffered samples are delivered at 60m, ahead of the sample that occurred at 60m only by
	// their earlier occurrence; what matters is that they arrive AFTER nothing that occurred before
	// 60m is left, i.e. in order here, and BEFORE the 70m sample.
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("delivery order %v, want %v", order, want)
	}
	// With a second device that keeps reporting, the buffered samples are visibly late.
	y := baseScenario + "  - token: d2\n    streams:\n      - {metric: temperature, every: 10m, constant: 1}\nfaults:\n  - offline: {device: d1, from: 30m, for: 30m}\n"
	g = gen(t, mustScenario(t, y))
	var seen []string
	late := 0
	var newest time.Time
	for _, e := range g.Events {
		if e.At.Before(newest) {
			late++
			seen = append(seen, fmt.Sprintf("%s@%s", e.Device, e.At.Sub(start)))
		}
		if e.At.After(newest) {
			newest = e.At
		}
	}
	if late != 2 { // d1@30m and d1@40m arrive behind d2@50m; d1@50m ties with it
		t.Fatalf("late deliveries = %d (%v), want 2", late, seen)
	}
}

func TestDuplicateAtProbabilityOneDoublesEveryEvent(t *testing.T) {
	g := gen(t, mustScenario(t, baseScenario+"faults:\n  - duplicate: {probability: 1}\n"))
	if len(g.Events) != 26 {
		t.Fatalf("events = %d, want 26", len(g.Events))
	}
	for i := 0; i < len(g.Events); i += 2 {
		if !reflect.DeepEqual(g.Events[i], g.Events[i+1]) {
			t.Fatalf("events %d and %d are not copies of each other", i, i+1)
		}
	}
}

func TestDuplicateIsSeededAndPartial(t *testing.T) {
	y := strings.Replace(baseScenario, "duration: 2h", "duration: 48h", 1) + "faults:\n  - duplicate: {probability: 0.3}\n"
	a, b := gen(t, mustScenario(t, y)), gen(t, mustScenario(t, y))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("not deterministic")
	}
	base := len(gen(t, mustScenario(t, strings.Replace(baseScenario, "duration: 2h", "duration: 48h", 1))).Events)
	extra := len(a.Events) - base
	if extra < base/10 || extra > base/2 {
		t.Fatalf("%d duplicates of %d events at probability 0.3", extra, base)
	}
}

func TestReorderDelaysDelivery(t *testing.T) {
	y := strings.Replace(baseScenario, "duration: 2h", "duration: 24h", 1) + "faults:\n  - reorder: {probability: 0.5, maxDelay: 45m}\n"
	g := gen(t, mustScenario(t, y))
	late := 0
	var newest time.Time
	for _, e := range g.Events {
		if e.At.Before(newest) {
			late++
		} else {
			newest = e.At
		}
	}
	if late == 0 {
		t.Fatal("a reorder fault at probability 0.5 reordered nothing")
	}
	// Every event is still delivered exactly once.
	if len(g.Events) != 24*6+1 {
		t.Fatalf("events = %d", len(g.Events))
	}
	if !reflect.DeepEqual(g, gen(t, mustScenario(t, y))) {
		t.Fatal("not deterministic")
	}
}

func TestEventLimitIsEnforcedWithAHelpfulMessage(t *testing.T) {
	y := strings.Replace(strings.Replace(baseScenario, "every: 10m", "every: 1s", 1), "duration: 2h", "duration: 30d", 1)
	_, err := mustScenario(t, y).Generate(nil)
	if err == nil || !strings.Contains(err.Error(), "every") {
		t.Fatalf("err = %v, want a refusal naming `every`", err)
	}
}

func TestSafeJoin(t *testing.T) {
	if p, err := SafeJoin("/a/b", "c/d.csv"); err != nil || !strings.HasSuffix(strings.ReplaceAll(p, "\\", "/"), "a/b/c/d.csv") {
		t.Fatalf("SafeJoin = %q, %v", p, err)
	}
	for _, bad := range []string{"", "../x", "c/../../x", "/etc/passwd", "..", "C:\\x"} {
		if p, err := SafeJoin("/a/b", bad); err == nil && bad != "C:\\x" {
			t.Errorf("SafeJoin(%q) = %q, want a refusal", bad, p)
		}
	}
}

// An oversized stream is refused before it is generated: `every: 1ns` over two hours would be
// seven trillion samples, and allocating them first would exhaust memory before the limit spoke.
func TestOversizedStreamIsRefusedBeforeGeneration(t *testing.T) {
	y := strings.Replace(baseScenario, "every: 10m", "every: 1ns", 1)
	done := make(chan error, 1)
	go func() { _, err := mustScenario(t, y).Generate(nil); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "every") {
			t.Fatalf("err = %v, want a refusal naming `every`", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Generate did not refuse an oversized stream before generating it")
	}
}
