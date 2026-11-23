// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

func TestFoundationExecutesBaseline(t *testing.T) {
	for _, scenario := range []string{"pass", "probe fails", "package fails after pass", "no tests", "tag error", "worktree error", "remove error"} {
		t.Run(scenario, func(t *testing.T) {
			added, removed, ran := false, false, false
			git := func(args ...string) ([]byte, error) {
				if args[0] == "tag" {
					return []byte("v1.8.2\nv1.9.0\nv1.8.3-rc.1"), nil
				}
				if args[0] == "rev-parse" && scenario == "tag error" {
					return nil, fmtError("missing tag")
				}
				if args[0] == "worktree" && args[1] == "add" {
					added = true
					if scenario == "worktree error" {
						return nil, fmtError("cannot add")
					}
					if args[2] != "--detach" || args[4] != "v1.8.2" {
						t.Fatalf("wrong baseline checkout: %v", args)
					}
					return nil, os.MkdirAll(filepath.Join(args[3], "backend", "cli", "bootstrap"), 0700)
				}
				if args[0] == "worktree" && args[1] == "remove" {
					removed = true
					if scenario == "remove error" {
						return nil, fmtError("cannot remove")
					}
				}
				// A declaration-only foundation must not bypass the go probe.
				return []byte("package bootstrap\nfunc RefuseForPendingOperation() error { return nil }"), nil
			}
			runner := func(dir, name string, args ...string) ([]byte, error) {
				ran = true
				if name != "go" || strings.Join(args, " ") != "test -count=1 -v -run ^TestFoundationProbe$ ./bootstrap" {
					t.Fatalf("wrong probe command: %s %v", name, args)
				}
				got, err := os.ReadFile(filepath.Join(dir, "bootstrap", "foundation_probe_test.go"))
				if err != nil {
					t.Fatal(err)
				}
				want, err := os.ReadFile("../../cli/bootstrap/foundation_probe_test.go")
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Fatal("probe source changed during copy")
				}
				if scenario == "probe fails" {
					return []byte("--- FAIL: TestFoundationProbe"), fmtError("refusal ineffective or request API missing")
				}
				if scenario == "package fails after pass" {
					// The probe passed but the package run did not (e.g. a TestMain or
					// init failure): a non-zero exit must refuse whatever the output says.
					return []byte("--- PASS: TestFoundationProbe (0.00s)\nFAIL"), fmtError("exit status 1")
				}
				if scenario == "no tests" {
					return []byte("testing: warning: no tests to run\nPASS"), nil
				}
				return []byte("--- PASS: TestFoundationProbe (0.00s)\nPASS"), nil
			}
			err := requireRolledFoundation(Registry{api.ActionUpgrade: nil}, "", "v1.9.0", git, runner)
			if (err == nil) != (scenario == "pass") {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
			if scenario != "tag error" && !added {
				t.Error("baseline was not checked out")
			}
			if ran && !removed {
				t.Error("worktree was not removed")
			}
			if scenario == "pass" && !ran {
				t.Error("baseline refusal never executed")
			}
		})
	}
}
