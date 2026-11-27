// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper process: this test binary, re-run as one of the roles below. A role is
// chosen by environment, which the command under test inherits from the test.
const (
	procTreeRoleEnv = "DCCTL_PROCTREE_ROLE"
	procTreePidsEnv = "DCCTL_PROCTREE_PIDS"
	// procTreeLine starts the one line of output a role that reports writes, so the
	// test framework's own output around it is never mistaken for it.
	procTreeLine = "proctree:"
)

// procTreeArgs runs TestProcTreeHelper, and nothing else, in this test binary.
var procTreeArgs = []string{"-test.run=^TestProcTreeHelper$", "-test.count=1"}

// TestProcTreeHelper is the helper's body. It does nothing unless a test started it.
//
//   - child: a command that spawns a grandchild holding its stdout and stderr open (an
//     exec credential plugin that kubectl started), records both pids, and stalls.
//   - grandchild: stalls.
//   - pgid: reports its pid, its process group and whether its stdin is the null device
//     (Unix; see preflight_proctree_unix_test.go).
func TestProcTreeHelper(t *testing.T) {
	switch os.Getenv(procTreeRoleEnv) {
	case "":
		return
	case "child":
		gc := exec.Command(os.Args[0], procTreeArgs...)
		gc.Env = append(os.Environ(), procTreeRoleEnv+"=grandchild")
		gc.Stdout, gc.Stderr = os.Stdout, os.Stderr
		if err := gc.Start(); err != nil {
			os.Exit(3)
		}
		pids := os.Getenv(procTreePidsEnv)
		if err := os.WriteFile(pids+".tmp", fmt.Appendf(nil, "%d %d\n", os.Getpid(), gc.Process.Pid), 0o600); err != nil {
			os.Exit(4)
		}
		if err := os.Rename(pids+".tmp", pids); err != nil {
			os.Exit(4)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	case "grandchild":
		time.Sleep(time.Minute)
		os.Exit(0)
	default:
		procTreeReport()
		os.Exit(0)
	}
}

// 🔴 ENDING A SCREEN COMMAND'S CONTEXT ENDS EVERYTHING IT SPAWNED. A cancelled command
// whose descendant still held its output was already given up on after
// commandWaitDelay; the descendant itself must not live on as an orphan. Both the
// command and its grandchild are gone within commandWaitDelay of the cancel, and the
// join returns.
func TestABoundCommandsWholeProcessTreeEndsWithItsContext(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	t.Setenv(procTreeRoleEnv, "child")
	t.Setenv(procTreePidsEnv, pids)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		runContext(ctx, os.Args[0], procTreeArgs...)
	}()
	var child, grandchild int
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pids); err == nil {
			if _, err := fmt.Sscan(string(b), &child, &grandchild); err != nil {
				t.Fatalf("the helper recorded %q: %v", b, err)
			}
			break
		}
		select {
		case <-joined:
			t.Fatal("the command ended before it had spawned its grandchild")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never spawned its grandchild")
		}
	}
	childGone, grandchildGone := watchExit(t, child), watchExit(t, grandchild)
	if childGone() || grandchildGone() {
		t.Fatal("the helper's tree ended before the cancel")
	}

	cancel()
	cancelled := time.Now()
	for !childGone() || !grandchildGone() {
		if time.Since(cancelled) > commandWaitDelay {
			t.Fatalf("%v after the cancel: command gone %v, grandchild gone %v", commandWaitDelay,
				childGone(), grandchildGone())
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-joined:
	case <-time.After(commandWaitDelay + 3*time.Second - time.Since(cancelled)):
		t.Fatal("the command was not joined after its tree had ended")
	}
}

// parseProcTreeReport finds the helper's report line in out.
func parseProcTreeReport(t *testing.T, out string) []string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), procTreeLine); ok {
			return strings.Fields(rest)
		}
	}
	t.Fatalf("the helper reported nothing: %q", out)
	return nil
}
