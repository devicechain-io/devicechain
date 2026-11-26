// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package buildcheck holds the deliberately separate, build-only release check.
// Run it through mcp-gate independently of the fast policy tests and mutants.
// The underscore directory excludes this expensive check from normal test runs.
package buildcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSixReleaseTargets(t *testing.T) {
	module, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"/"+arch, func(t *testing.T) {
				// Match run_gate's standalone module mode. The audit package
				// separately checks workspace selections and checksum coverage.
				env := append(os.Environ(), "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=readonly")
				// Build the entire CLI, then compile (never execute) the
				// test-only Charm surface, including teatest, for that target.
				for _, args := range [][]string{
					{"build", "-p=2", "./..."},
					{"test", "-c", "-p=2", "-o", filepath.Join(t.TempDir(), "stack.test"), "./internal/installtui"},
				} {
					cmd := exec.Command("go", args...)
					cmd.Dir, cmd.Env = module, env
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("go %v: %v\n%s", args, err, output)
					}
				}
			})
		}
	}
}
