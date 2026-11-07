// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-mcp/arena"
)

func valid() *McpConfiguration {
	c := NewMcpConfiguration()
	c.ResourceUrl = "https://mcp.example.com/api/mcp"
	c.IssuerUrl = "https://as.example.com/user-management"
	return c
}

// The experimental arena is OFF unless the configuration says otherwise, and "says
// otherwise" means an explicit enabled: true.
func TestTheArenaIsOffByDefault(t *testing.T) {
	for name, c := range map[string]*McpConfiguration{
		"absent": valid(),
		"present but not enabled": func() *McpConfiguration {
			c := valid()
			c.ExperimentalArena = &ExperimentalArena{ScenarioDir: "../arena/testdata"}
			return c
		}(),
	} {
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		opts, err := c.ArenaOptions()
		if err != nil || opts.Arena != nil {
			t.Fatalf("%s: arena options = %+v, %v; want none", name, opts, err)
		}
	}
}

func TestEnablingTheArenaNeedsAScenarioDirectoryThatLoads(t *testing.T) {
	c := valid()
	c.ExperimentalArena = &ExperimentalArena{Enabled: true}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "scenarioDir") {
		t.Fatalf("enabled with no scenarioDir: %v", err)
	}

	c.ExperimentalArena.ScenarioDir = t.TempDir()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ArenaOptions(); err == nil {
		t.Fatal("an empty scenario directory was accepted; the service must refuse to start on it")
	}

	c.ExperimentalArena.ScenarioDir = "../arena/testdata"
	opts, err := c.ArenaOptions()
	if err != nil || opts.Arena == nil {
		t.Fatalf("a good directory was refused: %v", err)
	}
}

func TestAPolicyMustNameASuiteTheScenariosHave(t *testing.T) {
	c := valid()
	c.ExperimentalArena = &ExperimentalArena{Enabled: true, ScenarioDir: "../arena/testdata",
		Policy: arena.Policy{Rules: []arena.PolicyRule{{Suite: "no-such-suite", MinScore: 0.8, Commands: []string{"set-setpoint"}}}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ArenaOptions(); err == nil || !strings.Contains(err.Error(), "no-such-suite") {
		t.Fatalf("a policy for a suite that does not exist: %v", err)
	}

	for name, rule := range map[string]arena.PolicyRule{
		"no commands":     {Suite: "s", MinScore: 0.8},
		"score above one": {Suite: "s", MinScore: 1.5, Commands: []string{"x"}},
		"negative unsafe": {Suite: "s", MinScore: 0.5, MaxUnsafe: -1, Commands: []string{"x"}},
	} {
		c.ExperimentalArena.Policy = arena.Policy{Rules: []arena.PolicyRule{rule}}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
