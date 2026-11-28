// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"os/exec"
	"strings"
	"testing"
)

// 🔴 THE SWEEP MARK IS THE FIRST ENTRY OF THE ENVIRONMENT, whatever comes after it: the
// sweep reads only the start of each process's environment, so a mark behind a large
// one would be invisible. A mark the environment already carried (dcctl itself run under
// a sweep) is dropped, not left to win exec's last-value-wins dedup.
func TestTheSweepMarkLeadsTheEnvironment(t *testing.T) {
	big := "BIG=" + strings.Repeat("x", 2*environCapForTest)
	cmd := exec.Command("true")
	cmd.Env = []string{"A=1", sweepEnv + "=stale", big, "B=2"}
	m := mark(cmd)
	if m == "" {
		t.Fatal("no mark was made")
	}
	if cmd.Env[0] != m {
		t.Fatalf("the mark is not the first entry: %q", cmd.Env[0][:min(len(cmd.Env[0]), 40)])
	}
	n := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, sweepEnv+"=") {
			n++
		}
	}
	if n != 1 || len(cmd.Env) != 4 || cmd.Env[1] != "A=1" || cmd.Env[2] != big || cmd.Env[3] != "B=2" {
		t.Fatalf("the environment was not kept around the mark: %d marks, %d entries", n, len(cmd.Env))
	}

	// With no environment of its own, the command inherits dcctl's, still behind the mark.
	t.Setenv("DCCTL_PROCTREE_MARK_TEST", "kept")
	cmd = exec.Command("true")
	m = mark(cmd)
	if cmd.Env[0] != m {
		t.Fatal("the mark is not first in an inherited environment")
	}
	found := false
	for _, kv := range cmd.Env {
		found = found || kv == "DCCTL_PROCTREE_MARK_TEST=kept"
	}
	if !found {
		t.Fatal("the inherited environment was not kept")
	}
}

// environCapForTest is the sweep's cap (64 KiB), restated so this test runs on every
// platform, not only where the sweep is built.
const environCapForTest = 64 << 10

// The escrow passphrase never reaches a marked command, whether its environment is its
// own or dcctl's inherited one.
func TestTheSweepMarkDropsTheEscrowPassphrase(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"A=1", "DCCTL_ESCROW_PASSPHRASE=own"}
	mark(cmd)
	t.Setenv("DCCTL_ESCROW_PASSPHRASE", "inherited")
	inherited := exec.Command("true")
	mark(inherited)
	for _, c := range []*exec.Cmd{cmd, inherited} {
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, "DCCTL_ESCROW_PASSPHRASE=") {
				t.Fatalf("a marked command carries %q", kv)
			}
		}
	}
	if len(cmd.Env) != 2 || cmd.Env[1] != "A=1" {
		t.Fatalf("the rest of the environment was not kept: %q", cmd.Env)
	}
}
