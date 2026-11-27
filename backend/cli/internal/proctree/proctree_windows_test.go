// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const helperMarkerEnv = "DCCTL_PROCTREE_TEST_MARKER"

// TestHelperProcess is the command the tests run: it marks that it ran, then stalls. It
// does nothing unless a test started it.
func TestHelperProcess(t *testing.T) {
	marker := os.Getenv(helperMarkerEnv)
	if marker == "" {
		return
	}
	_ = os.WriteFile(marker, []byte("ran"), 0o600)
	time.Sleep(time.Minute)
	os.Exit(0)
}

// 🔴 A THREAD THAT IS NOT THE COMMAND'S IS NEVER RESUMED. A thread id from the snapshot
// can name another process's thread by the time it is opened; its owner is read from
// the opened handle and anything else is refused.
func TestAThreadOfAnotherProcessIsNeverResumed(t *testing.T) {
	self, tid := uint32(os.Getpid()), windows.GetCurrentThreadId()
	if err := resumeThread(self+4, tid); err == nil {
		t.Fatal("a thread of this process was resumed as another process's")
	}
	// The counterweight: the owner's own thread is resumed (a running thread's
	// suspend count is already zero, so this changes nothing).
	if err := resumeThread(self, tid); err != nil {
		t.Fatalf("the owner's thread was refused: %v", err)
	}
}

// 🔴 A COMMAND THAT CANNOT BE PUT IN A JOB NEVER RUNS. It is terminated while still
// suspended and Run returns an error; it is not resumed unconfined.
func TestACommandThatCannotBeConfinedNeverRuns(t *testing.T) {
	real := assignToJob
	t.Cleanup(func() { assignToJob = real })
	var refused windows.Handle
	assignToJob = func(_ windows.Handle, p windows.Handle) error {
		pid, err := windows.GetProcessId(p)
		if err != nil {
			return err
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			return err
		}
		refused = h
		return errors.New("assignment refused")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperMarkerEnv+"="+marker)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cmd) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run ran the command it could not confine")
	}
	if err == nil {
		t.Fatal("Run reported success for a command it could not confine")
	}
	if refused == 0 {
		t.Fatal("the assignment was never attempted")
	}
	defer func() {
		_ = windows.TerminateProcess(refused, 1)
		_ = windows.CloseHandle(refused)
	}()
	var code uint32
	if err := windows.GetExitCodeProcess(refused, &code); err != nil || code == 259 { // STILL_ACTIVE
		t.Fatalf("the command is still there (%v)", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}
