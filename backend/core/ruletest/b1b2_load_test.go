// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"strings"
	"testing"
)

// A scenario that can generate nothing is refused when it is read: a run over no events would pass
// every neverRaised expectation without the rules ever being asked.
func TestScenarioThatGeneratesNothingIsRefused(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"device with no streams": {strings.Replace(baseScenario, "    streams:\n      - metric: temperature\n        every: 10m\n        constant: 21.5\n",
			"    streams: []\n", 1), "no streams"},
		"stream starting at the end":   {baseScenario + "        from: 2h\n", "from must be before the scenario's duration"},
		"stream starting past the end": {baseScenario + "        from: 3h\n", "from must be before the scenario's duration"},
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

// Generate refuses an empty result even when every stream is well formed: a gap over the whole
// scenario leaves nothing to send.
func TestGenerateRefusesAnEmptyResult(t *testing.T) {
	s := mustScenario(t, baseScenario+"faults:\n  - gap: {from: 0s, for: 3h}\n")
	_, err := s.Generate(nil)
	if err == nil || !strings.Contains(err.Error(), "generates no events") {
		t.Fatalf("err = %v, want a refusal of a scenario that generates no events", err)
	}
}

// An expectation naming a device the scenario does not define is refused when the files are read:
// it could only ever be vacuously true (neverRaised) or certainly false (raised), and either way it
// tests a typo, not a rule.
func TestExpectationNamingAnUndefinedDeviceIsRefused(t *testing.T) {
	for _, block := range []string{
		"  - neverRaised: {device: d9}\n",
		"  - raised: {rule: hot, device: d9}\n",
		"  - neverIntent: {device: d9, kind: sendCommand}\n",
	} {
		dir := t.TempDir()
		write(t, dir, "scenario.yaml", baseScenario)
		write(t, dir, "t.test.yaml", "apiVersion: devicechain.io/ruletest/v1alpha1\nkind: RuleTest\nexpect:\n"+block)
		plan, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if s := plan.Suites[0]; s.Err == nil || !strings.Contains(s.Err.Error(), `device "d9" is not in the scenario`) {
			t.Fatalf("%s: suite err = %v, want the undefined device refused", strings.TrimSpace(block), s.Err)
		}
	}
}
