// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
)

// The experiment is off by default: a dcctl started without the flag neither lists nor can run
// the fork command, so the served command surface is unchanged.
func TestExperimentalForkCommandIsAbsentByDefault(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "fork" {
			t.Fatalf("the experimental fork command is registered without %s=true", experimentalForkTimelineEnv)
		}
	}
}

func TestParsePerturbation(t *testing.T) {
	m, err := parsePerturbation("metric-offset,metric=temperature,offset=5,scale=2,from=2026-11-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if m["op"] != "metric-offset" || m["metric"] != "temperature" || m["offset"] != 5.0 || m["scale"] != 2.0 {
		t.Fatalf("parsed %v", m)
	}
	for _, bad := range []string{"", "device=x", "drop-device,device", "drop-device,from=yesterday", "metric-offset,offset=hot", "drop-device,colour=red"} {
		if _, err := parsePerturbation(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestRenderForkNoDivergence(t *testing.T) {
	var b strings.Builder
	renderFork(&b, &forkResult{RuleCount: 1})
	if !strings.Contains(b.String(), "No divergence") {
		t.Fatalf("got %q", b.String())
	}
}
