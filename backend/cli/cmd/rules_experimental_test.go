// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "testing"

// The experiment is off by default: a dcctl started without the flag neither lists nor can run
// the rules commands, so the served command surface is unchanged.
func TestExperimentalRulesCommandsAreAbsentByDefault(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "rules" {
			t.Fatalf("the experimental rules command is registered without %s=true", experimentalPhysicalPREnv)
		}
	}
}

func TestParseSince(t *testing.T) {
	end := timeNow().UTC()
	if got, err := parseSince("24h", end); err != nil || !got.Equal(end.Add(-24*60*60*1e9)) {
		t.Fatalf("24h: %v %v", got, err)
	}
	if got, err := parseSince("2026-11-01T00:00:00Z", end); err != nil || got.Day() != 1 {
		t.Fatalf("rfc3339: %v %v", got, err)
	}
	for _, bad := range []string{"", "-1h", "0s", "yesterday"} {
		if _, err := parseSince(bad, end); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
