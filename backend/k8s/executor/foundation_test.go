// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type gitRunner func(...string) ([]byte, error)
type foundationRunner func(dir, name string, args ...string) ([]byte, error)

func gitOutput(args ...string) ([]byte, error) { return exec.Command("git", args...).Output() }

func stableVersion(tag string) ([3]int, bool) {
	var v [3]int
	if !strings.HasPrefix(tag, "v") {
		return v, false
	}
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return v, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
func lessVersion(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func requireRolledFoundation(registry Registry, baseline, current string, git gitRunner, runners ...foundationRunner) (resultErr error) {
	if len(registry) == 0 {
		return nil
	}
	if baseline == "" {
		// A source build has no target tag; like upgrade-gate.yml it uses the newest stable release.
		target, ok := stableVersion(strings.SplitN(current, "-", 2)[0])
		if current != "" && !ok {
			return fmt.Errorf("invalid current version %q", current)
		}
		tags, err := git("tag", "-l")
		if err != nil {
			return fmt.Errorf("resolving baseline tags: %w", err)
		}
		var highest [3]int
		for _, tag := range strings.Fields(string(tags)) {
			v, ok := stableVersion(tag)
			if ok && (current == "" || lessVersion(v, target)) && (baseline == "" || lessVersion(highest, v)) {
				baseline = tag
				highest = v
			}
		}
	}
	if baseline == "" {
		return fmt.Errorf("no stable baseline below %s; fetch release tags or set DC_BASELINE_TAG", current)
	}
	if _, err := git("rev-parse", "--verify", baseline+"^{commit}"); err != nil {
		return fmt.Errorf("resolving baseline %s: %w", baseline, err)
	}
	if _, err := git("show", baseline+":backend/k8s/executor/registry.go"); err != nil {
		return fmt.Errorf("baseline %s lacks the executor foundation: %w", baseline, err)
	}
	probe, err := os.ReadFile("../../cli/bootstrap/foundation_probe_test.go")
	if err != nil {
		return fmt.Errorf("reading current foundation probe: %w", err)
	}
	scratch, err := os.MkdirTemp("", "dc-foundation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	tree := filepath.Join(scratch, "baseline")
	if output, err := git("worktree", "add", "--detach", tree, baseline); err != nil {
		return fmt.Errorf("checking out baseline %s: %w: %s", baseline, err, output)
	}
	defer func() {
		if output, err := git("worktree", "remove", "--force", tree); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("removing baseline worktree: %w: %s", err, output))
		}
	}()
	cli := filepath.Join(tree, "backend", "cli")
	if err := os.WriteFile(filepath.Join(cli, "bootstrap", "foundation_probe_test.go"), probe, 0600); err != nil {
		return fmt.Errorf("copying foundation probe into baseline %s: %w", baseline, err)
	}
	run := foundationRunner(func(dir, name string, args ...string) ([]byte, error) {
		command := exec.Command(name, args...)
		command.Dir = dir
		return command.CombinedOutput()
	})
	if len(runners) != 0 {
		run = runners[0]
	}
	output, err := run(cli, "go", "test", "-count=1", "-v", "-run", "^TestFoundationProbe$", "./bootstrap")
	if err != nil {
		return fmt.Errorf("baseline %s refusal probe failed: %w: %s", baseline, err, output)
	}
	if !strings.Contains(string(output), "--- PASS: TestFoundationProbe (") || strings.Contains(strings.ToLower(string(output)), "no tests to run") {
		return fmt.Errorf("baseline %s did not execute and pass TestFoundationProbe: %s", baseline, output)
	}
	return nil
}
