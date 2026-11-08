// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"strings"
	"testing"
	"time"
)

func at(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }

func parseTest(t *testing.T, body string) *TestFile {
	t.Helper()
	f, err := ParseTestFile([]byte("apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n" + body))
	if err != nil {
		t.Fatalf("ParseTestFile: %v", err)
	}
	return f
}

func judge(t *testing.T, body string, res Result) []Outcome {
	t.Helper()
	return Judge(parseTest(t, body), res, start, 2*time.Hour)
}

var fixture = Result{
	Evaluated: []string{"hot", "cold"},
	Edges: []Edge{
		{At: at(30), Series: "d1", Rule: "hot", Kind: "threshold", Raise: true},
		{At: at(50), Series: "d1", Rule: "hot", Kind: "threshold", Raise: false},
		{At: at(90), Series: "d2", Rule: "hot", Kind: "threshold", Raise: true},
	},
	Intents: []Intent{
		{At: at(30), Series: "d1", Rule: "hot", Raise: true, Kind: "raiseAlarm", AlarmKey: "overheat"},
		{At: at(30), Series: "d1", Rule: "hot", Raise: true, Kind: "sendCommand", Command: "set-fan", Payload: `{"speed":3,"mode":"auto"}`},
		{At: at(50), Series: "d1", Rule: "hot", Raise: false, Kind: "clearAlarm", AlarmKey: "overheat"},
		{At: at(90), Series: "d2", Rule: "hot", Raise: true, Kind: "httpCall", URL: "https://x.example/h", Body: "device=d2"},
		{At: at(90), Series: "d2", Rule: "hot", Raise: true, Kind: "publish", Connector: "plant", Body: "hot:d2"},
	},
}

func want(t *testing.T, got []Outcome, passed ...bool) {
	t.Helper()
	if len(got) != len(passed) {
		t.Fatalf("outcomes = %+v, want %d", got, len(passed))
	}
	for i, p := range passed {
		if got[i].Passed != p {
			t.Errorf("expect[%d] %q passed=%v, want %v: %s", i, got[i].Name, got[i].Passed, p, got[i].Message)
		}
	}
}

func TestRaisedAndClearedWindows(t *testing.T) {
	got := judge(t, `
  - raised: {rule: hot, device: d1}
  - raised: {rule: hot, device: d1, within: 30m}
  - raised: {rule: hot, device: d1, within: 29m}
  - raised: {rule: hot, device: d1, after: 31m}
  - raised: {rule: hot, device: d2, after: 1h, within: 1h}
  - raised: {rule: cold}
  - raised: {device: nobody}
  - cleared: {rule: hot, device: d1, within: 50m}
  - cleared: {rule: hot, device: d1, within: 49m}
  - cleared: {rule: hot, device: d2}
`, fixture)
	want(t, got, true, true, false, false, true, false, false, true, false, false)
}

func TestWindowBoundsAreInclusive(t *testing.T) {
	// The edge is at exactly +30m: within 30m includes it, and so does after 30m.
	got := judge(t, `
  - raised: {device: d1, within: 30m}
  - raised: {device: d1, after: 30m, within: 1m}
`, fixture)
	want(t, got, true, true)
}

func TestCount(t *testing.T) {
	got := judge(t, `
  - raised: {rule: hot, count: 2}
  - raised: {rule: hot, count: 1}
  - raised: {rule: hot, count: 0, device: nobody}
  - raised: {rule: hot, device: d1, count: 1}
  - cleared: {count: 1}
  - cleared: {count: 2}
`, fixture)
	want(t, got, true, false, true, true, true, false)
}

func TestNeverRaised(t *testing.T) {
	got := judge(t, `
  - neverRaised: {rule: cold}
  - neverRaised: {rule: hot}
  - neverRaised: {rule: hot, device: d2, within: 1h}
  - neverRaised: {rule: hot, device: d2, after: 1h}
  - neverRaised: {device: nobody}
`, fixture)
	want(t, got, true, false, true, false, true)
	if !strings.Contains(got[1].Message, "raised 2 time(s)") || !strings.Contains(got[1].Message, "first at 2026-01-01T00:30:00Z on d1") {
		t.Fatalf("message = %q", got[1].Message)
	}
}

func TestIntentMatching(t *testing.T) {
	got := judge(t, `
  - intent: {kind: sendCommand, command: set-fan}
  - intent: {kind: sendCommand, command: set-fanx}
  - intent: {kind: sendCommand, command: set-fan, payload: {speed: 3, mode: auto}}
  - intent: {kind: sendCommand, command: set-fan, payload: {mode: auto, speed: 3}}
  - intent: {kind: sendCommand, command: set-fan, payload: {speed: 4, mode: auto}}
  - intent: {kind: sendCommand, command: set-fan, payload: {speed: 3}}
  - intent: {kind: raiseAlarm, alarmKey: overheat}
  - intent: {kind: raiseAlarm, alarmKey: other}
  - intent: {kind: clearAlarm, alarmKey: overheat, device: d1}
  - intent: {kind: clearAlarm, alarmKey: overheat, device: d2}
  - intent: {kind: httpCall, url: "https://x.example/h", body: "device=d2"}
  - intent: {kind: httpCall, bodyContains: "d2"}
  - intent: {kind: httpCall, bodyContains: "d3"}
  - intent: {kind: publish, connector: plant, body: "hot:d2"}
  - intent: {kind: publish, connector: other}
  - intent: {edge: resolved}
  - intent: {edge: resolved, kind: raiseAlarm}
  - intent: {edge: raised, kind: sendCommand, within: 30m}
  - intent: {edge: raised, kind: sendCommand, within: 29m}
  - intent: {kind: sendCommand, count: 1}
  - intent: {kind: sendCommand, count: 2}
  - intent: {rule: hot}
  - intent: {rule: cold}
`, fixture)
	want(t, got, true, false, true, true, false, false, true, false, true, false, true, true, false, true, false,
		true, false, true, false, true, false, true, false)
}

func TestEmptyPayloadEqualsAnEmptyObject(t *testing.T) {
	res := Result{Evaluated: []string{"r"}, Intents: []Intent{{At: at(1), Series: "d", Rule: "r", Kind: "sendCommand", Command: "reboot", Payload: ""}}}
	got := judge(t, `
  - intent: {command: reboot, payload: {}}
  - intent: {command: reboot, payload: {force: true}}
`, res)
	want(t, got, true, false)
}

func TestNeverIntent(t *testing.T) {
	got := judge(t, `
  - neverIntent: {kind: sendCommand, command: shutdown}
  - neverIntent: {kind: sendCommand, command: set-fan}
  - neverIntent: {kind: sendCommand, command: set-fan, after: 1h}
`, fixture)
	want(t, got, true, false, true)
}

func TestNamesAreDefaultedAndOverridable(t *testing.T) {
	got := judge(t, `
  - name: the boiler overheats
    raised: {rule: hot, device: d1}
  - raised: {rule: hot, device: d1}
  - intent: {kind: sendCommand, command: set-fan}
`, fixture)
	if got[0].Name != "the boiler overheats" {
		t.Fatalf("name = %q", got[0].Name)
	}
	if got[1].Name != "rule hot on device d1 raised" {
		t.Fatalf("name = %q", got[1].Name)
	}
	if got[2].Name != `emits sendCommand, command "set-fan" from any rule on any device` {
		t.Fatalf("name = %q", got[2].Name)
	}
}

func TestFailureMessagesSayWhatWasFound(t *testing.T) {
	got := judge(t, `
  - raised: {rule: hot, device: d1, after: 1h}
  - raised: {rule: cold}
  - intent: {kind: sendCommand, command: nope}
`, fixture)
	if !strings.Contains(got[0].Message, "outside it, first at 2026-01-01T00:30:00Z") {
		t.Errorf("msg0 = %q", got[0].Message)
	}
	if !strings.Contains(got[1].Message, "never raised in the whole scenario") || !strings.Contains(got[1].Message, "3 edge(s) in all") {
		t.Errorf("msg1 = %q", got[1].Message)
	}
	if !strings.Contains(got[2].Message, `sendCommand "set-fan"`) {
		t.Errorf("msg2 = %q", got[2].Message)
	}
	empty := judge(t, "  - intent: {kind: sendCommand}\n", Result{})
	if !strings.Contains(empty[0].Message, "no intent at all") {
		t.Errorf("empty msg = %q", empty[0].Message)
	}
	deg := Judge(parseTest(t, "  - raised: {rule: cold}\n"), Result{Evaluated: []string{"cold"}, Degraded: "3 readings arrived too far behind"}, start, time.Hour)
	if !strings.Contains(deg[0].Message, "the evaluation reported: 3 readings") {
		t.Errorf("degraded msg = %q", deg[0].Message)
	}
	ok := Judge(parseTest(t, "  - neverRaised: {rule: cold}\n    allowDegraded: true\n"), Result{Evaluated: []string{"cold"}, Degraded: "x"}, start, time.Hour)
	if !ok[0].Passed || ok[0].Message != "" {
		t.Errorf("a passing expectation must not carry the degraded note: %+v", ok[0])
	}
}

func TestParseTestFileRefusals(t *testing.T) {
	hdr := "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\n"
	cases := map[string]struct{ y, want string }{
		"no expect":    {hdr, "expect is required"},
		"empty expect": {hdr + "expect: []\n", "expect is required"},
		"empty block":  {hdr + "expect:\n  - name: x\n", "exactly one of raised"},
		"two blocks":   {hdr + "expect:\n  - raised: {}\n    cleared: {}\n", "exactly one of raised"},
		"unknown key":  {hdr + "expect:\n  - raised: {rul: hot}\n", "rul"},
		"bad kind":     {hdr + "expect:\n  - intent: {kind: shout}\n", "is not one of"},
		"bad edge":     {hdr + "expect:\n  - intent: {edge: both}\n", "raised or resolved"},
		"bad within":   {hdr + "expect:\n  - raised: {within: 0s}\n", "within must be positive"},
		"neg after":    {hdr + "expect:\n  - raised: {after: -1m}\n", "must not be negative"},
		"neg count":    {hdr + "expect:\n  - raised: {count: -1}\n", "count must not be negative"},
		"never count":  {hdr + "expect:\n  - neverRaised: {count: 1}\n", "takes no count"},
		"never icount": {hdr + "expect:\n  - neverIntent: {count: 1}\n", "takes no count"},
		"wrong kind":   {strings.Replace(hdr, "RuleTest", "Scenario", 1) + "expect:\n  - raised: {}\n", "kind must be RuleTest"},
		"bad version":  {"apiVersion: v2\nkind: RuleTest\nexpect:\n  - raised: {}\n", "not one this build reads"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTestFile([]byte(c.y))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}
