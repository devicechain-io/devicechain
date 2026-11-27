// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

type ctxMark struct{}

// hostSeams records which way each of the doctor's commands ran, and under which
// context, with the tools all present.
func hostSeams(t *testing.T) (plain, bound, unmarked *int) {
	t.Helper()
	saveRun, saveLook, saveHost, savePlain := runCmd, lookPath, hostCommand, plainCommand
	t.Cleanup(func() { runCmd, lookPath, hostCommand, plainCommand = saveRun, saveLook, saveHost, savePlain })
	runCmd = nil
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	var mu sync.Mutex
	plain, bound, unmarked = new(int), new(int), new(int)
	plainCommand = func(string, ...string) string {
		mu.Lock()
		defer mu.Unlock()
		*plain++
		return ""
	}
	hostCommand = func(ctx context.Context, _ string, _ ...string) string {
		mu.Lock()
		defer mu.Unlock()
		*bound++
		if ctx.Value(ctxMark{}) == nil {
			*unmarked++
		}
		return ""
	}
	return plain, bound, unmarked
}

// 🔴 THE INSTALL'S OWN PREFLIGHT RUNS ITS COMMANDS AS IT ALWAYS DID, whatever context
// the run hands it: an interrupted plain install must print exactly what it printed
// before, not a "docker daemon not reachable" made up by a killed `docker info`.
func TestTheInstallPreflightRunsItsCommandsAsAlways(t *testing.T) {
	plain, bound, _ := hostSeams(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // interrupted before the checks begin
	if _, err := doctorRunner("local", doctorOptsFor("", "lab", false)).RunPreflight(ctx,
		func(installflow.PreflightObservation) {}); err != nil {
		t.Fatal(err)
	}
	if *plain == 0 || *bound != 0 {
		t.Fatalf("%d commands run as always, %d bound to the run's context", *plain, *bound)
	}
}

// The terminal UI's screen runs the bound form: every command under the context it was
// given, so ending that context ends them.
func TestTheScreensPreflightRunsItsCommandsUnderItsContext(t *testing.T) {
	plain, bound, unmarked := hostSeams(t)
	r, ok := doctorRunner("local", doctorOptsFor("", "lab", false)).(interface {
		BoundToContext() installflow.PreflightRunner
	})
	if !ok {
		t.Fatal("the install preflight has no bound form for the screen")
	}
	ctx := context.WithValue(context.Background(), ctxMark{}, true)
	if _, err := r.BoundToContext().RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
		t.Fatal(err)
	}
	if *bound == 0 || *plain != 0 || *unmarked != 0 {
		t.Fatalf("%d bound commands (%d not under the screen's context), %d run as always", *bound, *unmarked, *plain)
	}
}

// A command is killed when its context ends.
func TestAHostCommandEndsWithItsContext(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on this host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	runContext(ctx, "sleep", "10")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the command outlived its context by %v", d)
	}
}

// A killed command whose descendant still holds its output open (an exec credential
// plugin that kubectl started) is given up on shortly after its context ends, not
// waited on until the descendant exits.
func TestAHostCommandIsNotHeldOpenByADescendant(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on this host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	// The grandchild inherits stdout and stderr and outlives its parent.
	runContext(ctx, "sh", "-c", "(sleep 20) & sleep 20")
	if d := time.Since(start); d > commandWaitDelay+5*time.Second {
		t.Fatalf("a descendant holding the output kept the command for %v", d)
	}
}
