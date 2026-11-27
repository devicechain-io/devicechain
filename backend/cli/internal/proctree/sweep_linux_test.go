// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a procfs root with n process entries, none of them dcctl's, each with an
// environ that does not carry the mark. Their pids are past any pid_max, so nothing can
// be signalled through them.
func fakeProc(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, strconv.Itoa(1<<30+i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte("HOME=/root\x00PATH=/bin\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Not processes: skipped without a read.
	_ = os.Mkdir(filepath.Join(root, "self"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "uptime"), []byte("1 1"), 0o600)
	return root
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	old := procRoot
	t.Cleanup(func() { procRoot = old })
	procRoot = root
}

const testMark = sweepEnv + "=0123456789abcdef0123456789abcdef"

// A sweep that has scanned everything says so.
func TestASweepThatFinishesIsComplete(t *testing.T) {
	withProcRoot(t, fakeProc(t, 200))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !sweep(ctx, testMark) {
		t.Fatal("a sweep with time to spare reads incomplete")
	}
}

// 🔴 A SWEEP IS BOUNDED: past its deadline it stops, however many processes are left, and
// says it is incomplete rather than claiming clean.
func TestASweepStopsAtItsDeadlineAndSaysSo(t *testing.T) {
	withProcRoot(t, fakeProc(t, 4000))
	// Long enough to get the slot and start, far too short to read 4000 environments.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel()
	start := time.Now()
	complete := sweep(ctx, testMark)
	if complete {
		t.Error("a sweep past its deadline reads complete")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("a sweep past its deadline took %v", took)
	}
}

// 🔴 AN ENVIRON IS READ ONLY UP TO environCap: the mark is a short, fixed-length entry,
// and an environment of megabytes is not read to its end. A mark past the cap is not
// seen; one inside it is.
func TestAnEnvironIsReadOnlyUpToTheCap(t *testing.T) {
	root := t.TempDir()
	withProcRoot(t, root)
	write := func(pid int, env string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pad := strings.Repeat("X", 4*environCap)
	write(1, "PAD="+pad+"\x00"+testMark+"\x00")
	write(2, testMark+"\x00PAD="+pad+"\x00")
	write(3, "A=1\x00"+testMark)
	write(4, "A=1\x00"+testMark+"0\x00") // a longer value is not the mark
	// The cap falls just after what looks like the mark, inside a longer entry: what was
	// cut off is not known, so it is not the mark.
	write(5, "PAD="+strings.Repeat("X", environCap-5-len(testMark))+"\x00"+testMark+"0\x00")
	for pid, want := range map[int]bool{1: false, 2: true, 3: true, 4: false, 5: false} {
		if got := marked(pid, testMark); got != want {
			t.Errorf("pid %d: marked %v, want %v", pid, got, want)
		}
	}
}

// 🔴 SWEEPS NEVER RUN AT ONCE: a second waits for the first, and one whose deadline passes
// while it waits stops, incomplete, without scanning.
func TestSweepsAreSerialised(t *testing.T) {
	withProcRoot(t, fakeProc(t, 10))
	sweepSlot <- struct{}{} // a sweep in progress
	released := false
	defer func() {
		if !released {
			<-sweepSlot
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if sweep(ctx, testMark) {
		t.Error("a sweep that never got its turn reads complete")
	}
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Errorf("the second sweep did not wait for the first (%v)", took)
	}
	<-sweepSlot
	released = true
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !sweep(ctx2, testMark) {
		t.Error("a sweep after the first ended reads incomplete")
	}
}

// RunWith says when its sweep did not finish: the command ran cleanly, but what it left
// was not all looked for, so the run is not reported clean.
func TestRunWithReportsAnIncompleteSweep(t *testing.T) {
	sweepSlot <- struct{}{} // another sweep holds the slot past this one's budget
	defer func() { <-sweepSlot }()
	err := RunWith(context.Background(), exec.Command("true"), Options{Sweep: true})
	if !errors.Is(err, ErrSweepIncomplete) {
		t.Fatalf("RunWith returned %v", err)
	}
	if err := RunWith(context.Background(), exec.Command("true"), Options{}); err != nil {
		t.Fatalf("without a sweep, RunWith returned %v", err)
	}
}

// The escape helper: this test binary, re-run. As "leader" it starts an "escaped" copy
// of itself in a session of its own (out of the command's process group, so the group
// kill cannot reach it) and exits once that copy has written its pid; as "escaped" it
// writes its pid and stalls. Both keep the environment they were given, mark and all.
const (
	escapeRoleEnv = "DCCTL_PROCTREE_ESCAPE_ROLE"
	escapePidEnv  = "DCCTL_PROCTREE_ESCAPE_PID"
)

func TestEscapeHelper(t *testing.T) {
	switch os.Getenv(escapeRoleEnv) {
	case "leader":
		c := exec.Command(os.Args[0], "-test.run=^TestEscapeHelper$", "-test.count=1")
		c.Env = append(os.Environ(), escapeRoleEnv+"=escaped")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		pidFile := os.Getenv(escapePidEnv)
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(pidFile); err == nil {
				os.Exit(0)
			}
		}
		os.Exit(4)
	case "escaped":
		pidFile := os.Getenv(escapePidEnv)
		if os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600) == nil {
			_ = os.Rename(pidFile+".tmp", pidFile)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// 🔴 A LARGE ENVIRONMENT DOES NOT HIDE AN ESCAPED DESCENDANT. The command is given an
// environment larger than the sweep reads of any process; a descendant that left the
// group (a session of its own) and kept running is still found by its mark and ended,
// and the sweep reports itself complete only because it was.
func TestASweepFindsAnEscapedDescendantBehindALargeEnvironment(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "escaped")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEscapeHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"DCCTL_PROCTREE_PAD="+strings.Repeat("p", environCap+environCap/2), // under MAX_ARG_STRLEN
		escapeRoleEnv+"=leader", escapePidEnv+"="+pidFile)
	if err := RunWith(context.Background(), cmd, Options{NewSession: true, EndTreeOnExit: true, Sweep: true}); err != nil {
		t.Fatalf("RunWith returned %v", err)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the escaped descendant never reported: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("the escaped descendant reported %q", b)
	}
	gone := func() bool {
		s, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		i := strings.LastIndexByte(string(s), ')')
		return i >= 0 && i+2 < len(s) && (s[i+2] == 'Z' || s[i+2] == 'X')
	}
	t.Cleanup(func() {
		if !gone() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// The sweep signalled it before RunWith returned; the kernel may take a moment to
	// finish it off.
	for deadline := time.Now().Add(5 * time.Second); !gone(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("an escaped descendant behind a large environment outlived the sweep")
		}
	}
}
